package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/amirashouri/tasktracker-server/internal/install"
)

type recordingRunner struct {
	steps []install.Step
	err   error
}

func (r *recordingRunner) Run(_ context.Context, step install.Step) error {
	r.steps = append(r.steps, step)
	return r.err
}

func runCLI(
	t *testing.T,
	args []string,
	stdin, statePath, buildVersion string,
	runner install.Runner,
) (int, string, string) {
	t.Helper()

	var stdout, stderr bytes.Buffer
	code := run(context.Background(), args, strings.NewReader(stdin), &stdout, &stderr, statePath, buildVersion, runner)
	return code, stdout.String(), stderr.String()
}

func seedState(t *testing.T, statePath string) install.State {
	t.Helper()

	dir := t.TempDir()
	state := install.State{
		Version:      install.StateVersion,
		InstanceName: "demo",
		Mode:         install.ModeSQLite,
		Provider:     install.ProviderLAN,
		DataDir:      dir,
		ComposePath:  filepath.Join(dir, "docker-compose.yml"),
		ImageTag:     "v1.2.3",
		ServerPort:   8080,
		JWTSecret:    strings.Repeat("a", 64),
		Path:         statePath,
	}
	if err := install.SaveState(state); err != nil {
		t.Fatalf("seed state: %v", err)
	}
	return state
}

func TestDryRunPrintsTheComposeAndCallsNothing(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "install.json")
	runner := &recordingRunner{}

	code, stdout, stderr := runCLI(t,
		[]string{"install", "--dry-run", "--data-dir", dir, "--name", "demo"},
		"", statePath, "v1.2.3", runner)

	if code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, stderr)
	}
	if len(runner.steps) != 0 {
		t.Errorf("dry-run executed %d steps, want none", len(runner.steps))
	}
	if !strings.Contains(stdout, "tasktracker-server:v1.2.3") {
		t.Errorf("dry-run output does not pin the build version:\n%s", stdout)
	}
	if _, err := os.Stat(statePath); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("dry-run wrote a state file: %v", err)
	}
}

func TestCorruptStateIsRefusedAndNotReplaced(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "install.json")
	const corrupt = `{"version":1,`
	if err := os.WriteFile(statePath, []byte(corrupt), 0o600); err != nil {
		t.Fatalf("write corrupt state: %v", err)
	}
	runner := &recordingRunner{}

	code, _, stderr := runCLI(t,
		[]string{"install", "--data-dir", dir, "--name", "demo"},
		"", statePath, "v1.2.3", runner)

	if code == 0 {
		t.Fatal("install succeeded with a corrupt state file")
	}
	if len(runner.steps) != 0 {
		t.Errorf("install ran %d steps before refusing the corrupt state", len(runner.steps))
	}
	if !strings.Contains(stderr, "not valid") {
		t.Errorf("stderr = %q, want a message about the invalid state", stderr)
	}
	data, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("read state after refusal: %v", err)
	}
	if string(data) != corrupt {
		t.Error("install replaced the corrupt state instead of refusing it")
	}
}

func TestUninstallRefusesAMismatchedName(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "install.json")
	state := seedState(t, statePath)
	runner := &recordingRunner{}

	code, _, stderr := runCLI(t, []string{"uninstall"}, "wrong\n", statePath, "v1.2.3", runner)

	if code == 0 {
		t.Fatal("uninstall accepted a mismatched instance name")
	}
	if len(runner.steps) != 0 {
		t.Errorf("uninstall removed %d things before refusing the name", len(runner.steps))
	}
	if !strings.Contains(stderr, "does not match") {
		t.Errorf("stderr = %q, want a name mismatch message", stderr)
	}
	if _, err := os.Stat(statePath); err != nil {
		t.Errorf("state file was removed after a refused uninstall: %v", err)
	}
	if state.InstanceName != "demo" {
		t.Fatal("test seed is wrong")
	}
}

func TestUninstallWithTheInstanceNamePlansTheRemoval(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "install.json")
	seedState(t, statePath)
	runner := &recordingRunner{}

	code, stdout, stderr := runCLI(t, []string{"uninstall"}, "demo\n", statePath, "v1.2.3", runner)

	if code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, stderr)
	}
	if len(runner.steps) != 2 {
		t.Fatalf("uninstall ran %d steps, want 2", len(runner.steps))
	}
	if runner.steps[0].Kind != install.StepComposeDown || runner.steps[1].Kind != install.StepRemoveState {
		t.Errorf("uninstall steps = %v, want compose down then state removal", runner.steps)
	}
	if !strings.Contains(stdout, "Data volumes were left in place") {
		t.Errorf("stdout = %q, want the data-retention note", stdout)
	}
}

func TestRepairDoesNotPrintTheSecretAgain(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "install.json")
	seedState(t, statePath)
	runner := &recordingRunner{}

	result := runInstallFlow(t, defaultInstallOptions(), statePath, "v2.0.0", runner, nil, false, nil, nil)

	if result.code != 0 {
		t.Fatalf("exit = %d, stderr = %s", result.code, result.stderr)
	}
	if strings.Contains(result.stdout, "JWT_SECRET=") {
		t.Error("repair printed the JWT secret a second time")
	}
	if !strings.Contains(result.stdout, "Repaired") {
		t.Errorf("stdout = %q, want a repair message", result.stdout)
	}
}

// `tt install` with no flags is what a customer runs to repair whatever is
// already there. It must read the mode and paths from the state rather than
// replacing a Postgres install with the new-install SQLite default.
func TestRepairWithoutFlagsPreservesAnExistingPostgresInstance(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "install.json")
	state := install.State{
		Version:          install.StateVersion,
		InstanceName:     "demo",
		Mode:             install.ModePostgres,
		Provider:         install.ProviderLAN,
		DataDir:          dir,
		ComposePath:      filepath.Join(dir, "docker-compose.yml"),
		ImageTag:         "v1.2.3",
		ServerPort:       8080,
		JWTSecret:        strings.Repeat("a", 64),
		DatabasePassword: strings.Repeat("b", 64),
		Path:             statePath,
	}
	if err := install.SaveState(state); err != nil {
		t.Fatalf("seed state: %v", err)
	}
	runner := &recordingRunner{}

	result := runInstallFlow(t, defaultInstallOptions(), statePath, "v2.0.0", runner, nil, false, nil, nil)
	if result.code != 0 {
		t.Fatalf("exit = %d, stderr = %s", result.code, result.stderr)
	}
	if len(runner.steps) == 0 {
		t.Fatal("repair executed no steps")
	}

	final := runner.steps[0].State
	if final.Mode != install.ModePostgres {
		t.Errorf("repair mode = %q, want postgres", final.Mode)
	}
	if final.JWTSecret != state.JWTSecret {
		t.Error("repair changed the JWT secret")
	}
	if final.ImageTag != state.ImageTag {
		t.Errorf("repair image tag = %q, want the installed %q", final.ImageTag, state.ImageTag)
	}
}

func TestFirstInstallPrintsTheSecret(t *testing.T) {
	dir := t.TempDir()
	runner := &recordingRunner{}
	opts := defaultInstallOptions()
	opts.dataDir = dir
	opts.name = "demo"
	opts.adminEmail = "admin@example.com"
	opts.adminPassword = "password123"

	result := runInstallFlow(t, opts, filepath.Join(dir, "install.json"), "v1.2.3", runner, nil, false, nil, func() string {
		return "192.168.1.50"
	})

	if result.code != 0 {
		t.Fatalf("exit = %d, stderr = %s", result.code, result.stderr)
	}
	if !strings.Contains(result.stdout, "JWT_SECRET=") {
		t.Errorf("first install did not print the secret:\n%s", result.stdout)
	}
	if !strings.Contains(result.stdout, "not printed again") {
		t.Errorf("first install did not say the secret is printed once:\n%s", result.stdout)
	}
}

func TestVersionCommand(t *testing.T) {
	code, stdout, stderr := runCLI(t, []string{"version"}, "", "/nonexistent/install.json", "v1.2.3", &recordingRunner{})
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, stderr)
	}
	if strings.TrimSpace(stdout) != "v1.2.3" {
		t.Errorf("version = %q, want v1.2.3", stdout)
	}
}

func TestUnknownCommandIsAUsageError(t *testing.T) {
	code, _, stderr := runCLI(t, []string{"frobnicate"}, "", "/nonexistent/install.json", "v1.2.3", &recordingRunner{})
	if code != 2 {
		t.Errorf("exit = %d, want 2", code)
	}
	if !strings.Contains(stderr, "unknown command") {
		t.Errorf("stderr = %q, want an unknown-command message", stderr)
	}
}
