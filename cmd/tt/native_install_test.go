package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/amirashouri/tasktracker-server/internal/install"
)

// nativeTestBinary writes an executable file the way the release bundle ships
// `server` beside `tt`.
func nativeTestBinary(t *testing.T) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "server")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write test binary: %v", err)
	}
	return path
}

func seedNativeState(t *testing.T, statePath string) install.State {
	t.Helper()

	dir := t.TempDir()
	state := install.State{
		Version:      install.StateVersion,
		Runtime:      install.RuntimeNative,
		InstanceName: "demo",
		Mode:         install.ModeSQLite,
		Provider:     install.ProviderLAN,
		DataDir:      dir,
		BinaryPath:   filepath.Join(dir, "server"),
		EnvPath:      filepath.Join(dir, "secrets.env"),
		UnitPath:     filepath.Join(dir, "demo.service"),
		ServerBind:   install.NativeLoopback,
		ImageTag:     "v1.2.3",
		ServerPort:   8080,
		JWTSecret:    strings.Repeat("a", 64),
		Path:         statePath,
	}
	if err := install.SaveState(state); err != nil {
		t.Fatalf("seed native state: %v", err)
	}
	if err := os.WriteFile(state.EnvPath, []byte(install.RenderNativeEnv(state)), 0o600); err != nil {
		t.Fatalf("seed env file: %v", err)
	}
	return state
}

// writingRunner applies the steps that write a file, so an expose test can
// assert the env file on disk without a real systemctl. Everything else is
// recorded.
type writingRunner struct {
	steps []install.Step
	err   error
}

func (r *writingRunner) Run(_ context.Context, step install.Step) error {
	r.steps = append(r.steps, step)
	if r.err != nil {
		return r.err
	}
	if step.Kind == install.StepWriteEnv {
		return os.WriteFile(step.State.EnvPath, []byte(step.Env), 0o600)
	}
	return nil
}

func nativeDeps(
	t *testing.T,
	p prompter,
	runner *recordingRunner,
	interactive bool,
	stdout, stderr *bytes.Buffer,
) (installDeps, *install.PreflightOptions, *bool) {
	t.Helper()

	preflightSeen := &install.PreflightOptions{}
	dockerCalled := false

	deps := installDeps{
		stdout:       stdout,
		stderr:       stderr,
		statePath:    filepath.Join(t.TempDir(), "install.json"),
		buildVersion: "v1.2.3",
		runner:       runner,
		preflight: func(_ context.Context, opts install.PreflightOptions) error {
			*preflightSeen = opts
			return nil
		},
		ensureDocker: func(context.Context, install.Executor, install.Prompter, install.PreflightOptions) error {
			dockerCalled = true
			return nil
		},
		executor:    install.RealExecutor{},
		prompter:    p,
		lanAddress:  func() string { return "" },
		interactive: interactive,
	}
	return deps, preflightSeen, &dockerCalled
}

// The wizard's "without Docker" answer is the only thing that turns the native
// runtime on, and it must reach the plan without Docker ever being consulted.
func TestWizardInstallsNativeWithoutDocker(t *testing.T) {
	binary := nativeTestBinary(t)
	dir := t.TempDir()
	p := &scriptedPrompter{
		confirms: []bool{true}, // "Run the server without Docker?"
		asks:     []string{"sqlite", dir, "Admin"},
		required: []string{"admin@example.com"},
		secrets:  []string{"password123", "password123"},
	}
	runner := &recordingRunner{}
	var stdout, stderr bytes.Buffer
	deps, preflightSeen, dockerCalled := nativeDeps(t, p, runner, true, &stdout, &stderr)

	opts := defaultInstallOptions()
	opts.dataDir = dir
	opts.serverBinary = binary

	code := installWith(context.Background(), opts, deps)
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, stderr.String())
	}
	if *dockerCalled {
		t.Error("a native install consulted Docker")
	}
	if preflightSeen.Runtime != install.RuntimeNative {
		t.Errorf("preflight runtime = %q, want native", preflightSeen.Runtime)
	}
	if preflightSeen.BinaryPath != binary {
		t.Errorf("preflight binary = %q, want %q", preflightSeen.BinaryPath, binary)
	}

	var sawEnv, sawUnit bool
	for _, step := range runner.steps {
		switch step.Kind {
		case install.StepWriteEnv:
			sawEnv = true
		case install.StepWriteUnit:
			sawUnit = true
		case install.StepWriteCompose, install.StepComposeUp:
			t.Errorf("a native install ran the compose step %q", step.Kind)
		}
	}
	if !sawEnv || !sawUnit {
		t.Errorf("steps = %+v, want the env file and the unit written", runner.steps)
	}
	if !strings.Contains(stdout.String(), "systemctl status tasktracker.service") {
		t.Errorf("output does not name the service:\n%s", stdout.String())
	}
}

func TestInstallNativeNonInteractive(t *testing.T) {
	binary := nativeTestBinary(t)
	dir := t.TempDir()
	runner := &recordingRunner{}
	var stdout, stderr bytes.Buffer
	deps, preflightSeen, dockerCalled := nativeDeps(t, nil, runner, false, &stdout, &stderr)

	opts := defaultInstallOptions()
	opts.yes = true
	opts.runtime = string(install.RuntimeNative)
	opts.dataDir = dir
	opts.serverBinary = binary
	opts.adminEmail = "admin@example.com"
	opts.adminPassword = "password123"
	opts.platform = "linux"

	if code := installWith(context.Background(), opts, deps); code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, stderr.String())
	}
	if *dockerCalled {
		t.Error("a native install consulted Docker")
	}
	if preflightSeen.Runtime != install.RuntimeNative {
		t.Errorf("preflight runtime = %q, want native", preflightSeen.Runtime)
	}

	var kinds []install.StepKind
	for _, step := range runner.steps {
		kinds = append(kinds, step.Kind)
	}
	want := []install.StepKind{
		install.StepWriteState, install.StepEnsureUser, install.StepWriteEnv,
		install.StepWriteUnit, install.StepStartService, install.StepWaitReady,
		install.StepCreateAdmin,
	}
	if len(kinds) != len(want) {
		t.Fatalf("steps = %v, want %v", kinds, want)
	}
	for i := range want {
		if kinds[i] != want[i] {
			t.Errorf("step %d = %q, want %q", i, kinds[i], want[i])
		}
	}
}

func TestInstallRefusesNativePostgres(t *testing.T) {
	runner := &recordingRunner{}
	opts := defaultInstallOptions()
	opts.yes = true
	opts.runtime = string(install.RuntimeNative)
	opts.mode = string(install.ModePostgres)
	opts.dataDir = t.TempDir()
	opts.serverBinary = nativeTestBinary(t)
	opts.adminEmail = "admin@example.com"
	opts.adminPassword = "password123"

	result := runInstallFlow(t, opts, filepath.Join(t.TempDir(), "install.json"), "v1.2.3",
		runner, nil, false, nil, nil)

	if result.code == 0 {
		t.Fatal("a native postgres install was accepted")
	}
	if !strings.Contains(result.stderr, "SQLite only") {
		t.Errorf("stderr = %q, want the SQLite-only refusal", result.stderr)
	}
	if len(runner.steps) != 0 {
		t.Errorf("ran %d steps after a refusal", len(runner.steps))
	}
}

// tt expose --provider=lan on a native instance widens the bind in the env file
// and restarts the unit; there is no compose publish to rewrite.
func TestExposeNativeWidensTheBindAndRestarts(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "install.json")
	state := seedNativeState(t, statePath)
	runner := &writingRunner{}
	var verified string

	code, stdout, stderr := runExposeForTest(t, statePath, runner, "192.168.1.20",
		func(_ context.Context, address string) error {
			verified = address
			return nil
		})

	if code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, stderr)
	}
	if verified != "http://192.168.1.20:8080/readyz" {
		t.Errorf("verified %q, want the LAN address", verified)
	}
	if !strings.Contains(stdout, "Exposed on the LAN: http://192.168.1.20:8080") {
		t.Errorf("stdout does not print the LAN URL:\n%s", stdout)
	}

	var kinds []install.StepKind
	for _, step := range runner.steps {
		kinds = append(kinds, step.Kind)
	}
	want := []install.StepKind{install.StepWriteEnv, install.StepRestartService}
	if len(kinds) != len(want) {
		t.Fatalf("steps = %v, want %v", kinds, want)
	}
	for i := range want {
		if kinds[i] != want[i] {
			t.Errorf("step %d = %q, want %q", i, kinds[i], want[i])
		}
	}

	env, err := os.ReadFile(state.EnvPath)
	if err != nil {
		t.Fatalf("read env: %v", err)
	}
	if !strings.Contains(string(env), "SERVER_BIND=0.0.0.0") {
		t.Errorf("env does not carry the LAN bind:\n%s", env)
	}

	saved, err := install.LoadState(statePath)
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	if saved.ServerBind != install.NativeAllInterfaces {
		t.Errorf("saved bind = %q, want %q", saved.ServerBind, install.NativeAllInterfaces)
	}
}

// A failed expose must put the loopback env back, so a server that never
// answers is not left listening on every interface.
func TestExposeNativeRestoresTheBindWhenVerificationFails(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "install.json")
	state := seedNativeState(t, statePath)
	runner := &writingRunner{}

	code, _, stderr := runExposeForTest(t, statePath, runner, "192.168.1.20",
		func(context.Context, string) error { return os.ErrDeadlineExceeded })

	if code == 0 {
		t.Fatal("expose succeeded without a reachable LAN address")
	}
	if !strings.Contains(stderr, "no response") {
		t.Errorf("stderr = %q, want the verification failure", stderr)
	}
	env, err := os.ReadFile(state.EnvPath)
	if err != nil {
		t.Fatalf("read env: %v", err)
	}
	if !strings.Contains(string(env), "SERVER_BIND=127.0.0.1") {
		t.Errorf("env was not restored to loopback:\n%s", env)
	}
	saved, err := install.LoadState(statePath)
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	if saved.ServerBind != install.NativeLoopback {
		t.Errorf("saved bind = %q, want the loopback it still has", saved.ServerBind)
	}
}
