package install

import (
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
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
