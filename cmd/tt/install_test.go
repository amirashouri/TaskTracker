package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/amirashouri/tasktracker-server/internal/install"
)

type scriptedPrompter struct {
	asks     []string
	required []string
	secrets  []string
	confirms []bool
}

func (p *scriptedPrompter) Ask(_, _ string) (string, error) {
	if len(p.asks) == 0 {
		return "", io.EOF
	}
	value := p.asks[0]
	p.asks = p.asks[1:]
	return value, nil
}

func (p *scriptedPrompter) AskRequired(_ string) (string, error) {
	if len(p.required) == 0 {
		return "", io.EOF
	}
	value := p.required[0]
	p.required = p.required[1:]
	return value, nil
}

func (p *scriptedPrompter) Secret(_ string) (string, error) {
	if len(p.secrets) == 0 {
		return "", io.EOF
	}
	value := p.secrets[0]
	p.secrets = p.secrets[1:]
	return value, nil
}

func (p *scriptedPrompter) Confirm(_ string, _ bool) (bool, error) {
	if len(p.confirms) == 0 {
		return false, io.EOF
	}
	value := p.confirms[0]
	p.confirms = p.confirms[1:]
	return value, nil
}

type stepFuncRunner func(context.Context, install.Step) error

func (f stepFuncRunner) Run(ctx context.Context, step install.Step) error {
	return f(ctx, step)
}

type installFlowResult struct {
	code           int
	stdout, stderr string
	preflightCalls []install.PreflightOptions
}

func runInstallFlow(
	t *testing.T,
	opts installOptions,
	statePath, buildVersion string,
	runner install.Runner,
	p prompter,
	interactive bool,
	preflightErr error,
	lanAddress func() string,
) installFlowResult {
	t.Helper()

	var stdout, stderr bytes.Buffer
	var preflightCalls []install.PreflightOptions
	if opts.explicit == nil {
		opts.explicit = map[string]bool{}
	}
	deps := installDeps{
		stdout:       &stdout,
		stderr:       &stderr,
		statePath:    statePath,
		buildVersion: buildVersion,
		runner:       runner,
		preflight: func(_ context.Context, call install.PreflightOptions) error {
			preflightCalls = append(preflightCalls, call)
			return preflightErr
		},
		prompter:    p,
		lanAddress:  lanAddress,
		interactive: interactive,
	}

	code := installWith(context.Background(), opts, deps)
	return installFlowResult{
		code:           code,
		stdout:         stdout.String(),
		stderr:         stderr.String(),
		preflightCalls: preflightCalls,
	}
}

func TestWizardInteractiveInstallCreatesAnAdmin(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "install.json")
	runner := &recordingRunner{}
	p := &scriptedPrompter{
		asks:     []string{"postgres", dir, "Admin"},
		required: []string{"admin@example.com"},
		secrets:  []string{"password123", "password123"},
	}
	opts := defaultInstallOptions()
	opts.dataDir = dir

	result := runInstallFlow(t, opts, statePath, "v1.2.3", runner, p, true, nil, func() string {
		return "192.168.1.50"
	})

	if result.code != 0 {
		t.Fatalf("exit = %d, stderr = %s", result.code, result.stderr)
	}
	if len(result.preflightCalls) != 1 {
		t.Fatalf("preflight calls = %d, want 1", len(result.preflightCalls))
	}
	call := result.preflightCalls[0]
	if call.Mode != install.ModePostgres || call.DataDir != dir || call.Port != 8080 || call.Repair {
		t.Errorf("preflight = %+v, want postgres at %s on 8080", call, dir)
	}

	var adminStep *install.Step
	compose := ""
	for i := range runner.steps {
		switch runner.steps[i].Kind {
		case install.StepCreateAdmin:
			adminStep = &runner.steps[i]
		case install.StepWriteCompose:
			compose = runner.steps[i].Compose
		}
	}
	if adminStep == nil {
		t.Fatal("wizard did not plan an admin bootstrap")
	}
	if adminStep.Admin == nil || adminStep.Admin.Email != "admin@example.com" || adminStep.Admin.Password != "password123" || adminStep.Admin.DisplayName != "Admin" {
		t.Errorf("admin step = %+v, want the prompted credentials", adminStep.Admin)
	}
	if !strings.Contains(compose, "postgres:16-alpine") {
		t.Errorf("compose does not use postgres:\n%s", compose)
	}

	for _, want := range []string{
		"Installed",
		"JWT_SECRET=",
		"Admin: admin@example.com",
		"LAN address: 192.168.1.50",
		"laptop",
		"Next:",
	} {
		if !strings.Contains(result.stdout, want) {
			t.Errorf("stdout does not contain %q:\n%s", want, result.stdout)
		}
	}
}

func TestWizardPreflightFailureRunsNothing(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "install.json")
	runner := &recordingRunner{}
	opts := defaultInstallOptions()
	opts.dataDir = dir
	opts.adminEmail = "admin@example.com"
	opts.adminPassword = "password123"

	result := runInstallFlow(t, opts, statePath, "v1.2.3", runner, nil, false, errors.New("Docker is not installed"), nil)

	if result.code == 0 {
		t.Fatal("install succeeded despite a preflight failure")
	}
	if !strings.Contains(result.stderr, "Docker is not installed") {
		t.Errorf("stderr = %q, want the preflight failure", result.stderr)
	}
	if len(result.preflightCalls) != 1 {
		t.Errorf("preflight calls = %d, want 1", len(result.preflightCalls))
	}
	if len(runner.steps) != 0 {
		t.Errorf("runner executed %d steps after preflight failed", len(runner.steps))
	}
	if _, err := os.Stat(statePath); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("preflight failure wrote a state file: %v", err)
	}
}

func TestWizardDryRunCallsNoPreflightOrRunner(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "install.json")
	runner := &recordingRunner{}
	opts := defaultInstallOptions()
	opts.dataDir = dir
	opts.dryRun = true

	result := runInstallFlow(t, opts, statePath, "v1.2.3", runner, &scriptedPrompter{}, true, errors.New("preflight must not run"), nil)

	if result.code != 0 {
		t.Fatalf("exit = %d, stderr = %s", result.code, result.stderr)
	}
	if len(result.preflightCalls) != 0 {
		t.Errorf("dry-run called preflight %d times", len(result.preflightCalls))
	}
	if len(runner.steps) != 0 {
		t.Errorf("dry-run executed %d steps", len(runner.steps))
	}
	if !strings.Contains(result.stdout, "tasktracker-server:v1.2.3") {
		t.Errorf("dry-run output does not pin the build version:\n%s", result.stdout)
	}
	if _, err := os.Stat(statePath); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("dry-run wrote a state file: %v", err)
	}
}

func TestWizardNonInteractiveNeedsAdminFlagsBeforePreflight(t *testing.T) {
	dir := t.TempDir()
	runner := &recordingRunner{}
	opts := defaultInstallOptions()
	opts.dataDir = dir

	result := runInstallFlow(t, opts, filepath.Join(dir, "install.json"), "v1.2.3", runner, nil, false, errors.New("preflight must not run"), nil)

	if result.code == 0 {
		t.Fatal("non-interactive install succeeded without admin credentials")
	}
	if !strings.Contains(result.stderr, "--admin-email") || !strings.Contains(result.stderr, "--admin-password") {
		t.Errorf("stderr = %q, want both admin flags", result.stderr)
	}
	if len(result.preflightCalls) != 0 {
		t.Errorf("missing admin credentials reached preflight %d times", len(result.preflightCalls))
	}
	if len(runner.steps) != 0 {
		t.Errorf("missing admin credentials ran %d steps", len(runner.steps))
	}
}

func TestInstallFlagsFeedTheNonInteractiveAdminStep(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "install.json")
	runner := &recordingRunner{}
	var stdout, stderr bytes.Buffer
	var preflightCalls []install.PreflightOptions

	deps := installDeps{
		stdin:        strings.NewReader(""),
		stdout:       &stdout,
		stderr:       &stderr,
		statePath:    statePath,
		buildVersion: "v1.2.3",
		runner:       runner,
		preflight: func(_ context.Context, call install.PreflightOptions) error {
			preflightCalls = append(preflightCalls, call)
			return nil
		},
		prompter:   &scriptedPrompter{},
		lanAddress: func() string { return "192.168.1.50" },
	}

	code := runInstallWithDeps(context.Background(), []string{
		"--data-dir", dir,
		"--name", "demo",
		"--admin-email", "admin@example.com",
		"--admin-password", "password123",
		"--admin-display-name", "Admin",
		"--yes",
	}, deps)

	if code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, stderr.String())
	}
	if len(preflightCalls) != 1 || preflightCalls[0].Mode != install.ModeSQLite || preflightCalls[0].DataDir != dir {
		t.Errorf("preflight = %+v, want sqlite at %s", preflightCalls, dir)
	}
	var admin *install.Step
	for i := range runner.steps {
		if runner.steps[i].Kind == install.StepCreateAdmin {
			admin = &runner.steps[i]
		}
	}
	if admin == nil || admin.Admin == nil {
		t.Fatal("flags did not produce an admin step")
	}
	if admin.Admin.Email != "admin@example.com" || admin.Admin.Password != "password123" || admin.Admin.DisplayName != "Admin" {
		t.Errorf("admin = %+v, want the flag values", admin.Admin)
	}
	if !strings.Contains(stdout.String(), "Installed") {
		t.Errorf("stdout = %q, want an install message", stdout.String())
	}
	// No exposure provider has verified a URL in this task, so install must point
	// at the command that will produce one rather than print a link to loopback.
	if !strings.Contains(stdout.String(), "tt expose --provider=lan") {
		t.Errorf("stdout = %q, want the pairing hint", stdout.String())
	}
	if strings.Contains(stdout.String(), "tasktracker://") {
		t.Errorf("stdout = %q, want no pairing link without a verified URL", stdout.String())
	}
}

func TestWizardRepairAsksAndSkipsAdmin(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "install.json")
	state := seedState(t, statePath)
	runner := &recordingRunner{}
	p := &scriptedPrompter{confirms: []bool{true}}

	result := runInstallFlow(t, defaultInstallOptions(), statePath, "v2.0.0", runner, p, true, nil, nil)

	if result.code != 0 {
		t.Fatalf("exit = %d, stderr = %s", result.code, result.stderr)
	}
	if len(result.preflightCalls) != 1 {
		t.Fatalf("preflight calls = %d, want 1", len(result.preflightCalls))
	}
	call := result.preflightCalls[0]
	if !call.Repair || call.Mode != state.Mode || call.DataDir != state.DataDir || call.Port != state.ServerPort {
		t.Errorf("repair preflight = %+v, want the existing instance and Repair", call)
	}
	for _, step := range runner.steps {
		if step.Kind == install.StepCreateAdmin {
			t.Fatal("repair planned an admin bootstrap")
		}
	}
	if strings.Contains(result.stdout, "JWT_SECRET=") {
		t.Error("repair printed the JWT secret")
	}
	if !strings.Contains(result.stdout, "Repaired") {
		t.Errorf("stdout = %q, want a repair message", result.stdout)
	}
	if !strings.Contains(result.stdout, "tt expose --provider=lan") {
		t.Errorf("stdout = %q, want the pairing hint", result.stdout)
	}
	if strings.Contains(result.stdout, "tasktracker://") {
		t.Errorf("stdout = %q, want no pairing link without a verified URL", result.stdout)
	}
}

func TestWizardRepairDeclinedChangesNothing(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "install.json")
	seedState(t, statePath)
	runner := &recordingRunner{}
	p := &scriptedPrompter{confirms: []bool{false}}

	result := runInstallFlow(t, defaultInstallOptions(), statePath, "v2.0.0", runner, p, true, errors.New("preflight must not run"), nil)

	if result.code != 0 {
		t.Fatalf("exit = %d, stderr = %s", result.code, result.stderr)
	}
	if !strings.Contains(result.stdout, "Nothing changed") {
		t.Errorf("stdout = %q, want a no-change message", result.stdout)
	}
	if len(result.preflightCalls) != 0 || len(runner.steps) != 0 {
		t.Errorf("declined repair did work: preflight=%d steps=%d", len(result.preflightCalls), len(runner.steps))
	}
}

func TestWizardTailscaleProviderPointsAtExpose(t *testing.T) {
	dir := t.TempDir()
	runner := &recordingRunner{}
	opts := defaultInstallOptions()
	opts.dataDir = dir
	opts.provider = "tailscale"
	opts.adminEmail = "admin@example.com"
	opts.adminPassword = "password123"

	result := runInstallFlow(t, opts, filepath.Join(dir, "install.json"), "v1.2.3", runner, nil, false, nil, func() string {
		return "192.168.1.50"
	})

	if result.code != 0 {
		t.Fatalf("exit = %d, stderr = %s", result.code, result.stderr)
	}
	if !strings.Contains(result.stdout, "tt expose --provider=tailscale") {
		t.Errorf("stdout = %q, want the Tailscale next step", result.stdout)
	}
	if strings.Contains(result.stdout, "LAN address:") {
		t.Errorf("non-LAN install printed a LAN address:\n%s", result.stdout)
	}
}

func TestWizardCloudflareProviderPointsAtExpose(t *testing.T) {
	dir := t.TempDir()
	runner := &recordingRunner{}
	opts := defaultInstallOptions()
	opts.dataDir = dir
	opts.provider = "cloudflare"
	opts.adminEmail = "admin@example.com"
	opts.adminPassword = "password123"

	result := runInstallFlow(t, opts, filepath.Join(dir, "install.json"), "v1.2.3", runner, nil, false, nil, func() string {
		return "192.168.1.50"
	})

	if result.code != 0 {
		t.Fatalf("exit = %d, stderr = %s", result.code, result.stderr)
	}
	if !strings.Contains(result.stdout, "tt expose --provider=cloudflare --domain") {
		t.Errorf("stdout = %q, want the Cloudflare next step", result.stdout)
	}
	if strings.Contains(result.stdout, "LAN address:") {
		t.Errorf("non-LAN install printed a LAN address:\n%s", result.stdout)
	}
}

func TestWizardPrintsTheSecretBeforeAFailingAdminStep(t *testing.T) {
	dir := t.TempDir()
	var steps []install.Step
	runner := stepFuncRunner(func(_ context.Context, step install.Step) error {
		steps = append(steps, step)
		if step.Kind == install.StepCreateAdmin {
			return errors.New("admin bootstrap failed")
		}
		return nil
	})
	opts := defaultInstallOptions()
	opts.dataDir = dir
	opts.adminEmail = "admin@example.com"
	opts.adminPassword = "password123"

	result := runInstallFlow(t, opts, filepath.Join(dir, "install.json"), "v1.2.3", runner, nil, false, nil, func() string {
		return "192.168.1.50"
	})

	if result.code == 0 {
		t.Fatal("install succeeded despite a failed admin bootstrap")
	}
	if !strings.Contains(result.stdout, "JWT_SECRET=") {
		t.Errorf("stdout did not print the durable secret:\n%s", result.stdout)
	}
	if !strings.Contains(result.stderr, "admin bootstrap failed") {
		t.Errorf("stderr = %q, want the admin failure", result.stderr)
	}
	if len(steps) == 0 || steps[len(steps)-1].Kind != install.StepCreateAdmin {
		t.Errorf("steps = %v, want the admin step to be the failing one", steps)
	}
}

func TestValidateAdminFlags(t *testing.T) {
	cases := []struct {
		name    string
		opts    installOptions
		wantErr string
	}{
		{name: "missing", opts: installOptions{}, wantErr: "--admin-email"},
		{name: "invalid email", opts: installOptions{adminEmail: "not-an-email", adminPassword: "password123"}, wantErr: "not valid"},
		{name: "short password", opts: installOptions{adminEmail: "admin@example.com", adminPassword: "short"}, wantErr: "at least 8"},
		{name: "valid", opts: installOptions{adminEmail: "admin@example.com", adminPassword: "password123"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateAdminFlags(tc.opts)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("validateAdminFlags: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error = %v, want a reference to %q", err, tc.wantErr)
			}
		})
	}
}

func TestAskModeRejectsAnInvalidChoice(t *testing.T) {
	p := &scriptedPrompter{asks: []string{"mysql", "postgres"}}
	var out bytes.Buffer

	mode, err := askMode(p, &out, "sqlite")
	if err != nil {
		t.Fatalf("askMode: %v", err)
	}
	if mode != install.ModePostgres {
		t.Errorf("mode = %q, want postgres", mode)
	}
	if !strings.Contains(out.String(), "Choose sqlite or postgres") {
		t.Errorf("output = %q, want a choice hint", out.String())
	}
}

func TestAskAdminRetriesOnMismatch(t *testing.T) {
	p := &scriptedPrompter{
		asks:     []string{""},
		required: []string{"admin@example.com"},
		secrets:  []string{"password123", "wrong", "password123", "password123"},
	}
	var out bytes.Buffer

	admin, err := askAdmin(p, &out)
	if err != nil {
		t.Fatalf("askAdmin: %v", err)
	}
	if admin.Email != "admin@example.com" || admin.Password != "password123" {
		t.Errorf("admin = %+v, want the retried credentials", admin)
	}
	if !strings.Contains(out.String(), "Passwords do not match") {
		t.Errorf("output = %q, want a mismatch message", out.String())
	}
}
