package main

import (
	"bytes"
	"context"
	"errors"
	"net/url"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/amirashouri/tasktracker-server/internal/install"
)

type fakeCloudflare struct {
	pathErr   error
	createOut string
	createErr error
	routeErr  error
	calls     [][]string
}

func (f *fakeCloudflare) look() (string, error) {
	return "/usr/local/bin/cloudflared", f.pathErr
}

func (f *fakeCloudflare) run(_ context.Context, args ...string) (string, error) {
	f.calls = append(f.calls, append([]string(nil), args...))
	if len(args) > 0 && args[0] == "tunnel" && len(args) > 1 && args[1] == "create" {
		return f.createOut, f.createErr
	}
	if len(args) > 0 && args[0] == "tunnel" && len(args) > 1 && args[1] == "route" {
		return "", f.routeErr
	}
	return "", nil
}

func seedCloudflareState(t *testing.T, statePath string) install.State {
	t.Helper()

	state := seedState(t, statePath)
	state.Provider = install.ProviderCloudflare
	if err := install.SaveState(state); err != nil {
		t.Fatalf("save cloudflare state: %v", err)
	}
	return state
}

func runExposeCloudflareForTest(
	t *testing.T,
	statePath string,
	runner install.Runner,
	fake *fakeCloudflare,
	verify func(context.Context, string) error,
	args ...string,
) (int, string, string) {
	t.Helper()

	var stdout, stderr bytes.Buffer
	code := runExposeWithDeps(context.Background(), args, exposeDeps{
		stdout:    &stdout,
		stderr:    &stderr,
		statePath: statePath,
		runner:    runner,
		lanAddress: func() string {
			t.Error("LAN address detector called for a Cloudflare install")
			return ""
		},
		verify:         verify,
		lookTailscale:  func() (string, error) { return "", exec.ErrNotFound },
		runTailscale:   func(context.Context, ...string) (string, error) { return "", errors.New("not called") },
		lookCloudflare: fake.look,
		runCloudflare:  fake.run,
		goos:           "linux",
	})
	return code, stdout.String(), stderr.String()
}

func TestExposeCloudflareRequiresTheCLI(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "install.json")
	seedCloudflareState(t, statePath)
	runner := &recordingRunner{}
	fake := &fakeCloudflare{pathErr: exec.ErrNotFound}

	code, _, stderr := runExposeCloudflareForTest(t, statePath, runner, fake, nil, "--provider=cloudflare", "--domain=tasks.example.com")

	if code != 1 {
		t.Errorf("exit = %d, want 1", code)
	}
	if !strings.Contains(stderr, "cloudflared is not installed") {
		t.Errorf("stderr = %q, want the missing-CLI message", stderr)
	}
	if !strings.Contains(stderr, "apt install cloudflared") {
		t.Errorf("stderr = %q, want the Linux install command", stderr)
	}
	if len(runner.steps) != 0 || len(fake.calls) != 0 {
		t.Errorf("missing CLI ran %d compose steps and %d cloudflared calls, want none", len(runner.steps), len(fake.calls))
	}
}

func TestExposeCloudflareRequiresDomainFlag(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "install.json")
	seedCloudflareState(t, statePath)
	runner := &recordingRunner{}
	fake := &fakeCloudflare{}

	code, _, stderr := runExposeCloudflareForTest(t, statePath, runner, fake, nil, "--provider=cloudflare")

	if code != 1 {
		t.Errorf("exit = %d, want 1", code)
	}
	if !strings.Contains(stderr, "--domain is required") {
		t.Errorf("stderr = %q, want the missing-domain message", stderr)
	}
	if len(runner.steps) != 0 || len(fake.calls) != 0 {
		t.Errorf("missing domain ran %d compose steps and %d cloudflared calls, want none", len(runner.steps), len(fake.calls))
	}
}

func TestExposeCloudflareRefusesNonCloudflareZone(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "install.json")
	seedCloudflareState(t, statePath)
	runner := &recordingRunner{}
	fake := &fakeCloudflare{}

	code, _, stderr := runExposeCloudflareForTest(t, statePath, runner, fake, nil, "--provider=cloudflare", "--domain=not-on-cloudflare.invalid")

	if code != 1 {
		t.Errorf("exit = %d, want 1", code)
	}
	if !strings.Contains(stderr, "not on Cloudflare") {
		t.Errorf("stderr = %q, want the zone check message", stderr)
	}
	if !strings.Contains(stderr, "authoritative nameservers") {
		t.Errorf("stderr = %q, want the registrar guidance", stderr)
	}
	if len(runner.steps) != 0 || len(fake.calls) != 0 {
		t.Errorf("bad zone ran %d compose steps and %d cloudflared calls, want none", len(runner.steps), len(fake.calls))
	}
}

func TestExposeCloudflareCreatesTunnelAndPrintsPairingLink(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "install.json")
	seedCloudflareState(t, statePath)
	runner := &recordingRunner{}
	fake := &fakeCloudflare{createOut: "Tunnel created with id abc123-def456"}
	var verified string

	code, stdout, stderr := runExposeCloudflareForTest(t, statePath, runner, fake,
		func(_ context.Context, address string) error {
			verified = address
			return nil
		}, "--provider=cloudflare", "--domain=tasks.example.com")

	if code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, stderr)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want no output", stderr)
	}
	if verified != "https://tasks.example.com/readyz" {
		t.Errorf("verified %q, want the domain ready URL", verified)
	}
	if len(runner.steps) != 2 {
		t.Fatalf("ran %d steps, want write-compose and compose-up", len(runner.steps))
	}
	if runner.steps[0].Kind != install.StepWriteCompose || runner.steps[1].Kind != install.StepComposeUp {
		t.Errorf("steps = %v, want write-compose then compose-up", runner.steps)
	}
	compose := runner.steps[0].Compose
	if !strings.Contains(compose, `TRUSTED_PROXIES`) {
		t.Errorf("compose does not have trusted proxies:\n%s", compose)
	}
	if !strings.Contains(compose, `"127.0.0.1:8080:8080"`) {
		t.Errorf("compose does not stay on loopback:\n%s", compose)
	}
	if strings.Contains(compose, `"0.0.0.0:8080:8080"`) {
		t.Errorf("cloudflare expose published on every interface:\n%s", compose)
	}
	if !strings.Contains(compose, "cloudflared") {
		t.Errorf("compose missing cloudflared service:\n%s", compose)
	}

	wantCreate := []string{"tunnel", "create", "tasktracker-tasks.example.com"}
	wantRoute := []string{"tunnel", "route", "dns", "abc123-def456", "tasks.example.com"}
	if len(fake.calls) != 2 {
		t.Fatalf("cloudflared calls = %v, want tunnel create then route dns", fake.calls)
	}
	if !slices.Equal(fake.calls[0], wantCreate) {
		t.Errorf("call 0 = %v, want %v", fake.calls[0], wantCreate)
	}
	if !slices.Equal(fake.calls[1], wantRoute) {
		t.Errorf("call 1 = %v, want %v", fake.calls[1], wantRoute)
	}

	if !strings.Contains(stdout, "Exposed with Cloudflare Tunnel: https://tasks.example.com") {
		t.Errorf("stdout does not name the Cloudflare URL:\n%s", stdout)
	}
	wantLink := connectLinkPrefix + url.QueryEscape("https://tasks.example.com")
	if !strings.Contains(stdout, "Pairing link: "+wantLink) {
		t.Errorf("stdout has no pairing link %q:\n%s", wantLink, stdout)
	}
	if !strings.Contains(stdout, "cloudflared tunnel delete abc123-def456") {
		t.Errorf("stdout does not name the delete command:\n%s", stdout)
	}
}

func TestExposeCloudflareVerificationFailureDoesNotPrintPairingLink(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "install.json")
	seedCloudflareState(t, statePath)
	runner := &recordingRunner{}
	fake := &fakeCloudflare{createOut: "Tunnel created with id abc123-def456"}

	code, stdout, stderr := runExposeCloudflareForTest(t, statePath, runner, fake,
		func(context.Context, string) error { return errors.New("connection refused") }, "--provider=cloudflare", "--domain=tasks.example.com")

	if code != 1 {
		t.Errorf("exit = %d, want 1", code)
	}
	if strings.Contains(stdout, "Pairing link:") {
		t.Errorf("stdout printed a pairing link after a failed verification:\n%s", stdout)
	}
	if !strings.Contains(stderr, "https://tasks.example.com/readyz") {
		t.Errorf("stderr = %q, want the verified URL", stderr)
	}
	if !strings.Contains(stderr, "cloudflared tunnel delete") {
		t.Errorf("stderr = %q, want the cleanup command", stderr)
	}
	if len(runner.steps) != 2 {
		t.Errorf("ran %d steps, want only the compose apply (no automatic cleanup)", len(runner.steps))
	}
}

func TestExposeCloudflareTunnelCreateFailureIsReported(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "install.json")
	seedCloudflareState(t, statePath)
	runner := &recordingRunner{}
	fake := &fakeCloudflare{createErr: errors.New("authentication failed")}

	code, stdout, stderr := runExposeCloudflareForTest(t, statePath, runner, fake,
		func(context.Context, string) error {
			t.Error("verification ran after tunnel create failed")
			return nil
		}, "--provider=cloudflare", "--domain=tasks.example.com")

	if code != 1 {
		t.Errorf("exit = %d, want 1", code)
	}
	if !strings.Contains(stderr, "authentication failed") {
		t.Errorf("stderr = %q, want the command error", stderr)
	}
	if strings.Contains(stdout, "Pairing link:") {
		t.Errorf("stdout printed a pairing link after tunnel create failed:\n%s", stdout)
	}
}

func TestExposeCloudflareRefusesAProviderMismatch(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "install.json")
	seedState(t, statePath) // LAN state
	runner := &recordingRunner{}
	fake := &fakeCloudflare{}

	code, _, stderr := runExposeCloudflareForTest(t, statePath, runner, fake, nil, "--provider=cloudflare", "--domain=tasks.example.com")

	if code != 1 {
		t.Errorf("exit = %d, want 1", code)
	}
	if !strings.Contains(stderr, "configured for provider lan") {
		t.Errorf("stderr = %q, want the provider mismatch", stderr)
	}
	if !strings.Contains(stderr, "tt expose --provider=lan") {
		t.Errorf("stderr = %q, want the matching provider command", stderr)
	}
	if len(runner.steps) != 0 || len(fake.calls) != 0 {
		t.Errorf("mismatch ran %d compose steps and %d cloudflared calls, want none", len(runner.steps), len(fake.calls))
	}
}

func TestExposeCloudflareRefusesTailscaleState(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "install.json")
	seedTailscaleState(t, statePath) // Tailscale state
	runner := &recordingRunner{}
	fake := &fakeCloudflare{}

	code, _, stderr := runExposeCloudflareForTest(t, statePath, runner, fake, nil, "--provider=cloudflare", "--domain=tasks.example.com")

	if code != 1 {
		t.Errorf("exit = %d, want 1", code)
	}
	if !strings.Contains(stderr, "configured for provider tailscale") {
		t.Errorf("stderr = %q, want the provider mismatch", stderr)
	}
	if !strings.Contains(stderr, "tt expose --provider=tailscale") {
		t.Errorf("stderr = %q, want the matching provider command", stderr)
	}
	if len(runner.steps) != 0 || len(fake.calls) != 0 {
		t.Errorf("mismatch ran %d compose steps and %d cloudflared calls, want none", len(runner.steps), len(fake.calls))
	}
}

func TestCloudflareInstallHintNamesThePlatform(t *testing.T) {
	cases := map[string]string{
		"linux":   "apt install cloudflared",
		"darwin":  "brew install cloudflared",
		"windows": "winget install",
		"freebsd": "github.com/cloudflare/cloudflared",
	}
	for goos, want := range cases {
		t.Run(goos, func(t *testing.T) {
			if hint := cloudflareInstallHint(goos); !strings.Contains(hint, want) {
				t.Errorf("cloudflareInstallHint(%q) = %q, want %q", goos, hint, want)
			}
		})
	}
}
