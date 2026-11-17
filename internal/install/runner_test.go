package install

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

type recordingCommands struct {
	name string
	args []string
	err  error
}

func (r *recordingCommands) Run(_ context.Context, _, _ io.Writer, name string, args ...string) error {
	r.name = name
	r.args = append([]string(nil), args...)
	return r.err
}

func TestCreateAdminRunsUserCreateInTheServerContainer(t *testing.T) {
	commands := &recordingCommands{}
	runner := ExecRunner{commands: commands}
	state := State{InstanceName: "demo", ComposePath: "/tmp/docker-compose.yml"}
	step := Step{
		Kind:  StepCreateAdmin,
		State: state,
		Admin: &AdminBootstrap{
			Email:       "admin@example.com",
			Password:    "s3cret-password",
			DisplayName: "Admin",
		},
	}

	if err := runner.Run(context.Background(), step); err != nil {
		t.Fatalf("Run: %v", err)
	}

	want := []string{
		"compose", "--project-name", "demo", "-f", "/tmp/docker-compose.yml",
		"exec", "-T", "server", "/app/server", "user-create",
		"--email", "admin@example.com",
		"--password", "s3cret-password",
		"--display-name", "Admin",
		"--admin",
	}
	if commands.name != "docker" {
		t.Errorf("command = %q, want docker", commands.name)
	}
	if !reflect.DeepEqual(commands.args, want) {
		t.Errorf("args = %#v\nwant %#v", commands.args, want)
	}
}

func TestCreateAdminOmitsAnEmptyDisplayName(t *testing.T) {
	commands := &recordingCommands{}
	runner := ExecRunner{commands: commands}
	step := Step{
		Kind:  StepCreateAdmin,
		State: State{InstanceName: "demo", ComposePath: "/tmp/docker-compose.yml"},
		Admin: &AdminBootstrap{
			Email:    "admin@example.com",
			Password: "s3cret-password",
		},
	}

	if err := runner.Run(context.Background(), step); err != nil {
		t.Fatalf("Run: %v", err)
	}

	for _, arg := range commands.args {
		if arg == "--display-name" {
			t.Fatalf("args contain --display-name for an empty name: %#v", commands.args)
		}
	}
}

func TestCreateAdminErrorRedactsThePassword(t *testing.T) {
	commands := &recordingCommands{err: errors.New("exit status 1")}
	runner := ExecRunner{commands: commands}
	step := Step{
		Kind:  StepCreateAdmin,
		State: State{InstanceName: "demo", ComposePath: "/tmp/docker-compose.yml"},
		Admin: &AdminBootstrap{
			Email:    "admin@example.com",
			Password: "s3cret-password",
		},
	}

	err := runner.Run(context.Background(), step)
	if err == nil {
		t.Fatal("Run succeeded despite the command failure")
	}
	if strings.Contains(err.Error(), "s3cret-password") {
		t.Errorf("error leaked the admin password: %v", err)
	}
	if !strings.Contains(err.Error(), "[redacted]") {
		t.Errorf("error = %q, want a redacted password", err)
	}
}

func TestCreateAdminRequiresCredentials(t *testing.T) {
	commands := &recordingCommands{}
	runner := ExecRunner{commands: commands}

	err := runner.Run(context.Background(), Step{Kind: StepCreateAdmin})
	if err == nil {
		t.Fatal("Run accepted a create-admin step with no credentials")
	}
	if commands.name != "" {
		t.Errorf("Run invoked %q for an invalid admin step", commands.name)
	}
}

func TestWaitReadyReturnsOnA200(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	if err := WaitReady(context.Background(), server.URL, time.Second, 10*time.Millisecond); err != nil {
		t.Fatalf("WaitReady: %v", err)
	}
}

func TestWaitReadyTimesOutOnANon200(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()

	err := WaitReady(context.Background(), server.URL, 50*time.Millisecond, 10*time.Millisecond)
	if err == nil {
		t.Fatal("WaitReady accepted a non-200 response")
	}
	if !strings.Contains(err.Error(), server.URL) {
		t.Errorf("error %q does not name the address", err)
	}
}

// A cancelled run must stop the command it is running: the CLI's context is how
// Ctrl+C reaches docker, and a runner that ignores it leaves `tt install`
// waiting on a process nobody can see.
func TestExecCommandRunnerStopsOnCancellation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sleep is not available on Windows")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- execCommandRunner{}.Run(ctx, io.Discard, io.Discard, "sleep", "30")
	}()

	time.Sleep(100 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Run reported success for a cancelled command")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run kept the command alive after cancellation")
	}
}

func TestPullImageFetchesTheNamedReference(t *testing.T) {
	commands := &recordingCommands{}
	runner := ExecRunner{commands: commands}

	step := Step{Kind: StepPullImage, Image: "tasktracker-server:v1.4.0"}
	if err := runner.Run(context.Background(), step); err != nil {
		t.Fatalf("Run: %v", err)
	}

	want := []string{"pull", "tasktracker-server:v1.4.0"}
	if commands.name != "docker" {
		t.Errorf("command = %q, want docker", commands.name)
	}
	if !reflect.DeepEqual(commands.args, want) {
		t.Errorf("args = %#v\nwant %#v", commands.args, want)
	}
}

func TestPullImageRefusesAnEmptyReference(t *testing.T) {
	err := (ExecRunner{}).Run(context.Background(), Step{Kind: StepPullImage})
	if err == nil {
		t.Fatal("Run accepted a pull step with no image")
	}
}

func TestFetchBinaryStepDownloadsToTheSupervisedPath(t *testing.T) {
	original := FetchBinaryURL
	t.Cleanup(func() { FetchBinaryURL = original })

	var gotURL, gotDest string
	FetchBinaryURL = func(_ context.Context, url, dest string) error {
		gotURL, gotDest = url, dest
		return nil
	}

	state := State{BinaryPath: "/opt/tasktracker/server"}
	step := Step{
		Kind:      StepFetchBinary,
		State:     state,
		SourceURL: "https://example.com/tt/server",
	}
	if err := (ExecRunner{}).Run(context.Background(), step); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if gotURL != step.SourceURL {
		t.Errorf("fetched %q, want %q", gotURL, step.SourceURL)
	}
	if gotDest != state.BinaryPath {
		t.Errorf("wrote %q, want the supervised binary %q", gotDest, state.BinaryPath)
	}
}

func TestFetchBinaryStepRefusesAnEmptyURL(t *testing.T) {
	err := (ExecRunner{}).Run(context.Background(), Step{Kind: StepFetchBinary, State: State{BinaryPath: "/opt/tasktracker/server"}})
	if err == nil {
		t.Fatal("Run accepted a fetch step with no URL")
	}
}
