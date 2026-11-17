package main

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/amirashouri/tasktracker-server/internal/install"
)

func TestUpdateDockerPullsAndRecordsTheNewTag(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "install.json")
	seedState(t, statePath)
	runner := &recordingRunner{}

	code, stdout, stderr := runCLI(t, []string{"update", "--version", "v1.4.0"}, "", statePath, "dev", runner)
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, stderr)
	}

	want := []install.StepKind{
		install.StepPullImage,
		install.StepWriteState,
		install.StepWriteCompose,
		install.StepComposeUp,
		install.StepWaitReady,
	}
	if len(runner.steps) != len(want) {
		t.Fatalf("ran %d steps, want %d: %+v", len(runner.steps), len(want), runner.steps)
	}
	for i, kind := range want {
		if runner.steps[i].Kind != kind {
			t.Errorf("step %d = %q, want %q", i, runner.steps[i].Kind, kind)
		}
	}

	// The state write must already carry the new tag, or a crash between the
	// pull and the write leaves status claiming the old release.
	if got := install.StateFromSteps(runner.steps).ImageTag; got != "v1.4.0" {
		t.Errorf("planned tag = %q, want v1.4.0", got)
	}
	if runner.steps[0].Image != install.ImageRef("v1.4.0") {
		t.Errorf("pulled %q, want the new image", runner.steps[0].Image)
	}
	for _, wantLine := range []string{"Updated", "v1.4.0", "tt expose"} {
		if !strings.Contains(stdout, wantLine) {
			t.Errorf("stdout does not contain %q:\n%s", wantLine, stdout)
		}
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
}

func TestUpdateNativeFetchesTheBinaryAndRestarts(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "install.json")
	dir := t.TempDir()
	state := install.State{
		Version:      install.StateVersion,
		Runtime:      install.RuntimeNative,
		InstanceName: "demo",
		Mode:         install.ModeSQLite,
		Provider:     install.ProviderLAN,
		DataDir:      dir,
		BinaryPath:   "/opt/tasktracker/server",
		EnvPath:      "/etc/tasktracker/secrets.env",
		UnitPath:     "/etc/systemd/system/demo.service",
		ServerBind:   install.NativeLoopback,
		ImageTag:     "v1.2.3",
		ServerPort:   8080,
		JWTSecret:    strings.Repeat("a", 64),
		Path:         statePath,
	}
	if err := install.SaveState(state); err != nil {
		t.Fatalf("seed state: %v", err)
	}
	runner := &recordingRunner{}

	code, stdout, stderr := runCLI(t, []string{
		"update",
		"--server-binary-url", "https://example.com/tt/server",
		"--version", "v1.4.0",
	}, "", statePath, "dev", runner)
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, stderr)
	}

	want := []install.StepKind{
		install.StepFetchBinary,
		install.StepWriteState,
		install.StepRestartService,
		install.StepWaitReady,
	}
	if len(runner.steps) != len(want) {
		t.Fatalf("ran %d steps, want %d: %+v", len(runner.steps), len(want), runner.steps)
	}
	for i, kind := range want {
		if runner.steps[i].Kind != kind {
			t.Errorf("step %d = %q, want %q", i, runner.steps[i].Kind, kind)
		}
	}
	if !strings.Contains(stdout, "new binary") {
		t.Errorf("stdout does not say the service runs the new binary:\n%s", stdout)
	}
}

func TestUpdateDryRunExecutesNothing(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "install.json")
	seedState(t, statePath)
	runner := &recordingRunner{}

	code, stdout, stderr := runCLI(t, []string{"update", "--version", "v1.4.0", "--dry-run"}, "", statePath, "dev", runner)
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, stderr)
	}
	if len(runner.steps) != 0 {
		t.Errorf("dry run executed %d steps: %+v", len(runner.steps), runner.steps)
	}
	for _, want := range []string{"Would update", "Pull " + install.ImageRef("v1.4.0"), "Wait for /readyz"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout does not contain %q:\n%s", want, stdout)
		}
	}
}

func TestUpdateRefusesAnInstanceItCannotFind(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "install.json")

	code, _, stderr := runCLI(t, []string{"update", "--version", "v1.4.0"}, "", statePath, "dev", &recordingRunner{})
	if code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !strings.Contains(stderr, "no installation found") {
		t.Errorf("stderr = %q", stderr)
	}
}

func TestUpdateRejectsARuntimeItCannotServe(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "install.json")
	seedState(t, statePath)

	code, _, stderr := runCLI(
		t,
		[]string{"update", "--server-binary-url", "https://example.com/tt/server"},
		"", statePath, "dev", &recordingRunner{},
	)
	if code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !strings.Contains(stderr, "native instance") {
		t.Errorf("stderr = %q, want the refusal to fetch a binary for a Docker install", stderr)
	}
}
