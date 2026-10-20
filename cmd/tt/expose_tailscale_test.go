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

type fakeTailscale struct {
	pathErr   error
	status    string
	statusErr error
	runErr    error
	calls     [][]string
}

func (f *fakeTailscale) look() (string, error) {
	return "/usr/local/bin/tailscale", f.pathErr
}

func (f *fakeTailscale) run(_ context.Context, args ...string) (string, error) {
	f.calls = append(f.calls, append([]string(nil), args...))
	if len(args) > 0 && args[0] == "status" {
		return f.status, f.statusErr
	}
	return "", f.runErr
}

func seedTailscaleState(t *testing.T, statePath string) install.State {
	t.Helper()

	state := seedState(t, statePath)
	state.Provider = install.ProviderTailscale
	if err := install.SaveState(state); err != nil {
		t.Fatalf("save tailscale state: %v", err)
	}
	return state
}

func runExposeTailscaleForTest(
	t *testing.T,
	statePath string,
	runner install.Runner,
	fake *fakeTailscale,
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
			t.Error("LAN address detector called for a Tailscale install")
			return ""
		},
		verify:        verify,
		lookTailscale: fake.look,
		runTailscale:  fake.run,
		goos:          "linux",
	})
	return code, stdout.String(), stderr.String()
}

func TestExposeTailscaleRequiresTheCLI(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "install.json")
	seedTailscaleState(t, statePath)
	runner := &recordingRunner{}
	fake := &fakeTailscale{pathErr: exec.ErrNotFound}

	code, _, stderr := runExposeTailscaleForTest(t, statePath, runner, fake, nil, "--provider=tailscale")

	if code != 1 {
		t.Errorf("exit = %d, want 1", code)
	}
	if !strings.Contains(stderr, "Tailscale is not installed") {
		t.Errorf("stderr = %q, want the missing-CLI message", stderr)
	}
	if !strings.Contains(stderr, "https://tailscale.com/install.sh") {
		t.Errorf("stderr = %q, want the Linux install command", stderr)
	}
	if len(runner.steps) != 0 || len(fake.calls) != 0 {
		t.Errorf("missing CLI ran %d compose steps and %d tailscale calls, want none", len(runner.steps), len(fake.calls))
	}
}

func TestExposeTailscaleRequiresAnAuthenticatedTailnet(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "install.json")
	seedTailscaleState(t, statePath)
	runner := &recordingRunner{}
	fake := &fakeTailscale{status: `{"BackendState":"NeedsLogin","Self":{"DNSName":"host.tailnet.ts.net."},"CertDomains":["host.tailnet.ts.net"]}`}

	code, _, stderr := runExposeTailscaleForTest(t, statePath, runner, fake, nil, "--provider=tailscale")

	if code != 1 {
		t.Errorf("exit = %d, want 1", code)
	}
	if !strings.Contains(stderr, "tailscale up") {
		t.Errorf("stderr = %q, want the login guidance", stderr)
	}
	if len(runner.steps) != 0 {
		t.Errorf("ran %d compose steps before authentication", len(runner.steps))
	}
	if len(fake.calls) != 1 || fake.calls[0][0] != "status" {
		t.Errorf("tailscale calls = %v, want only status", fake.calls)
	}
}

func TestExposeTailscaleRequiresHTTPS(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "install.json")
	seedTailscaleState(t, statePath)
	runner := &recordingRunner{}
	fake := &fakeTailscale{status: `{"BackendState":"Running","Self":{"DNSName":"host.tailnet.ts.net."},"CertDomains":[]}`}

	code, _, stderr := runExposeTailscaleForTest(t, statePath, runner, fake, nil, "--provider=tailscale")

	if code != 1 {
		t.Errorf("exit = %d, want 1", code)
	}
	if !strings.Contains(stderr, "MagicDNS") || !strings.Contains(stderr, "HTTPS certificates") {
		t.Errorf("stderr = %q, want both dashboard steps", stderr)
	}
	if !strings.Contains(stderr, "API token") {
		t.Errorf("stderr = %q, want the no-automation caveat", stderr)
	}
	if len(runner.steps) != 0 {
		t.Errorf("ran %d compose steps without HTTPS", len(runner.steps))
	}
}

func TestExposeTailscalePublishesFunnelAndPrintsThePairingLink(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "install.json")
	seedTailscaleState(t, statePath)
	runner := &recordingRunner{}
	fake := &fakeTailscale{status: `{"BackendState":"Running","Self":{"DNSName":"host.tailnet.ts.net."},"CertDomains":["host.tailnet.ts.net"]}`}
	var verified string

	code, stdout, stderr := runExposeTailscaleForTest(t, statePath, runner, fake,
		func(_ context.Context, address string) error {
			verified = address
			return nil
		}, "--provider=tailscale")

	if code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, stderr)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want no output", stderr)
	}
	if verified != "https://host.tailnet.ts.net/readyz" {
		t.Errorf("verified %q, want the ts.net ready URL", verified)
	}
	if len(runner.steps) != 2 {
		t.Fatalf("ran %d steps, want write-compose and compose-up", len(runner.steps))
	}
	if runner.steps[0].Kind != install.StepWriteCompose || runner.steps[1].Kind != install.StepComposeUp {
		t.Errorf("steps = %v, want write-compose then compose-up", runner.steps)
	}
	compose := runner.steps[0].Compose
	if !strings.Contains(compose, `TRUSTED_PROXIES: "127.0.0.1/32"`) {
		t.Errorf("compose does not trust the loopback proxy:\n%s", compose)
	}
	if !strings.Contains(compose, `"127.0.0.1:8080:8080"`) {
		t.Errorf("compose does not stay on loopback:\n%s", compose)
	}
	if strings.Contains(compose, `"0.0.0.0:8080:8080"`) {
		t.Errorf("tailscale expose published on every interface:\n%s", compose)
	}

	wantFunnel := []string{"funnel", "--bg", "443", "http://127.0.0.1:8080"}
	if len(fake.calls) != 2 {
		t.Fatalf("tailscale calls = %v, want status then funnel", fake.calls)
	}
	if !slices.Equal(fake.calls[0], []string{"status", "--json"}) {
		t.Errorf("call 0 = %v, want status --json", fake.calls[0])
	}
	if !slices.Equal(fake.calls[1], wantFunnel) {
		t.Errorf("call 1 = %v, want %v", fake.calls[1], wantFunnel)
	}

	if !strings.Contains(stdout, "Exposed with Tailscale Funnel: https://host.tailnet.ts.net") {
		t.Errorf("stdout does not name the Funnel URL:\n%s", stdout)
	}
	wantLink := connectLinkPrefix + url.QueryEscape("https://host.tailnet.ts.net")
	if !strings.Contains(stdout, "Pairing link: "+wantLink) {
		t.Errorf("stdout has no pairing link %q:\n%s", wantLink, stdout)
	}
	if !strings.Contains(stdout, "tailscale funnel reset") {
		t.Errorf("stdout does not name the reset command:\n%s", stdout)
	}
}

func TestExposeTailscaleTailnetOnlyUsesServe(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "install.json")
	seedTailscaleState(t, statePath)
	runner := &recordingRunner{}
	fake := &fakeTailscale{status: `{"BackendState":"Running","Self":{"DNSName":"host.tailnet.ts.net."},"CertDomains":["host.tailnet.ts.net"]}`}

	code, stdout, stderr := runExposeTailscaleForTest(t, statePath, runner, fake,
		func(context.Context, string) error { return nil }, "--provider=tailscale", "--tailnet-only")

	if code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, stderr)
	}
	wantServe := []string{"serve", "--bg", "443", "http://127.0.0.1:8080"}
	if len(fake.calls) != 2 || !slices.Equal(fake.calls[1], wantServe) {
		t.Errorf("tailscale calls = %v, want status then %v", fake.calls, wantServe)
	}
	if !strings.Contains(stdout, "Tailscale Serve") {
		t.Errorf("stdout does not name Serve:\n%s", stdout)
	}
	if strings.Contains(stdout, "Tailscale Funnel") {
		t.Errorf("tailnet-only output names public Funnel:\n%s", stdout)
	}
	if !strings.Contains(stdout, "tailscale serve reset") {
		t.Errorf("stdout does not name the Serve reset command:\n%s", stdout)
	}
}

func TestExposeTailscaleVerificationFailureDoesNotPrintAPairingLink(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "install.json")
	seedTailscaleState(t, statePath)
	runner := &recordingRunner{}
	fake := &fakeTailscale{status: `{"BackendState":"Running","Self":{"DNSName":"host.tailnet.ts.net."},"CertDomains":["host.tailnet.ts.net"]}`}

	code, stdout, stderr := runExposeTailscaleForTest(t, statePath, runner, fake,
		func(context.Context, string) error { return errors.New("connection refused") }, "--provider=tailscale")

	if code != 1 {
		t.Errorf("exit = %d, want 1", code)
	}
	if strings.Contains(stdout, "Pairing link:") {
		t.Errorf("stdout printed a pairing link after a failed verification:\n%s", stdout)
	}
	if !strings.Contains(stderr, "https://host.tailnet.ts.net/readyz") {
		t.Errorf("stderr = %q, want the verified URL", stderr)
	}
	if !strings.Contains(stderr, "tailscale funnel reset") {
		t.Errorf("stderr = %q, want the reset command", stderr)
	}
	if len(runner.steps) != 2 {
		t.Errorf("ran %d steps, want only the compose apply (no automatic reset)", len(runner.steps))
	}
}

func TestExposeTailscaleCommandFailureIsReported(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "install.json")
	seedTailscaleState(t, statePath)
	runner := &recordingRunner{}
	fake := &fakeTailscale{
		status: `{"BackendState":"Running","Self":{"DNSName":"host.tailnet.ts.net."},"CertDomains":["host.tailnet.ts.net"]}`,
		runErr: errors.New("access denied"),
	}

	code, stdout, stderr := runExposeTailscaleForTest(t, statePath, runner, fake,
		func(context.Context, string) error {
			t.Error("verification ran after Funnel failed")
			return nil
		}, "--provider=tailscale")

	if code != 1 {
		t.Errorf("exit = %d, want 1", code)
	}
	if !strings.Contains(stderr, "access denied") || !strings.Contains(stderr, "admin console") {
		t.Errorf("stderr = %q, want the command error and Funnel guidance", stderr)
	}
	if strings.Contains(stdout, "Pairing link:") {
		t.Errorf("stdout printed a pairing link after Funnel failed:\n%s", stdout)
	}
}

func TestExposeTailscaleRefusesAProviderMismatch(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "install.json")
	seedState(t, statePath)
	runner := &recordingRunner{}
	fake := &fakeTailscale{status: `{"BackendState":"Running"}`}

	code, _, stderr := runExposeTailscaleForTest(t, statePath, runner, fake, nil, "--provider=tailscale")

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
		t.Errorf("mismatch ran %d compose steps and %d tailscale calls, want none", len(runner.steps), len(fake.calls))
	}
}

func TestExposeTailscaleRejectsAnUndecodableStatus(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "install.json")
	seedTailscaleState(t, statePath)
	runner := &recordingRunner{}
	fake := &fakeTailscale{status: "not json"}

	code, _, stderr := runExposeTailscaleForTest(t, statePath, runner, fake, nil, "--provider=tailscale")

	if code != 1 {
		t.Errorf("exit = %d, want 1", code)
	}
	if !strings.Contains(stderr, "decode tailscale status") {
		t.Errorf("stderr = %q, want the decode failure", stderr)
	}
	if len(runner.steps) != 0 {
		t.Errorf("ran %d compose steps after a bad status", len(runner.steps))
	}
}

func TestExposeTailscaleUsesTheInstalledPort(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "install.json")
	state := seedTailscaleState(t, statePath)
	state.ServerPort = 9090
	if err := install.SaveState(state); err != nil {
		t.Fatalf("save non-default port: %v", err)
	}
	runner := &recordingRunner{}
	fake := &fakeTailscale{status: `{"BackendState":"Running","Self":{"DNSName":"host.tailnet.ts.net."},"CertDomains":["host.tailnet.ts.net"]}`}

	code, _, stderr := runExposeTailscaleForTest(t, statePath, runner, fake,
		func(context.Context, string) error { return nil }, "--provider=tailscale")

	if code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, stderr)
	}
	want := []string{"funnel", "--bg", "443", "http://127.0.0.1:9090"}
	if len(fake.calls) != 2 || !slices.Equal(fake.calls[1], want) {
		t.Errorf("tailscale calls = %v, want status then %v", fake.calls, want)
	}
	if !strings.Contains(runner.steps[0].Compose, `"127.0.0.1:9090:8080"`) {
		t.Errorf("compose does not publish the installed port:\n%s", runner.steps[0].Compose)
	}
}

func TestTailscaleHTTPSName(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{
			name: "DNS name with the trailing root dot",
			body: `{"Self":{"DNSName":"host.tailnet.ts.net."},"CertDomains":["host.tailnet.ts.net"]}`,
			want: "host.tailnet.ts.net",
		},
		{
			name: "cert domain when DNS name is absent",
			body: `{"CertDomains":["host.tailnet.ts.net"]}`,
			want: "host.tailnet.ts.net",
		},
		{
			name: "magic DNS suffix fallback",
			body: `{"Self":{"HostName":"host"},"MagicDNSSuffix":"tailnet.ts.net","CertDomains":["host.tailnet.ts.net"]}`,
			want: "host.tailnet.ts.net",
		},
		{
			name: "a scheme is not a host",
			body: `{"Self":{"DNSName":"https://host.tailnet.ts.net"},"CertDomains":[]}`,
			want: "",
		},
		{
			name: "a path is not a host",
			body: `{"Self":{"DNSName":"host.tailnet.ts.net/readyz"},"CertDomains":[]}`,
			want: "",
		},
		{
			name: "a single label is not a tailnet name",
			body: `{"Self":{"DNSName":"host"},"CertDomains":[]}`,
			want: "",
		},
		{
			name: "empty status",
			body: `{}`,
			want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, err := parseTailscaleStatus(tc.body)
			if err != nil {
				t.Fatalf("parseTailscaleStatus: %v", err)
			}
			if got := status.httpsName(); got != tc.want {
				t.Errorf("httpsName() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestTailscaleExposeArgs(t *testing.T) {
	cases := []struct {
		name        string
		tailnetOnly bool
		port        int
		want        []string
		mode        string
		reset       string
	}{
		{
			name:  "public Funnel",
			port:  8080,
			want:  []string{"funnel", "--bg", "443", "http://127.0.0.1:8080"},
			mode:  "Funnel",
			reset: "tailscale funnel reset",
		},
		{
			name:        "tailnet-only Serve",
			tailnetOnly: true,
			port:        9090,
			want:        []string{"serve", "--bg", "443", "http://127.0.0.1:9090"},
			mode:        "Serve",
			reset:       "tailscale serve reset",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tailscaleExposeArgs(tc.tailnetOnly, tc.port); !slices.Equal(got, tc.want) {
				t.Errorf("tailscaleExposeArgs() = %v, want %v", got, tc.want)
			}
			if got := tailscaleModeName(tc.tailnetOnly); got != tc.mode {
				t.Errorf("tailscaleModeName() = %q, want %q", got, tc.mode)
			}
			if got := tailscaleResetCommand(tc.tailnetOnly); got != tc.reset {
				t.Errorf("tailscaleResetCommand() = %q, want %q", got, tc.reset)
			}
		})
	}
}

func TestTailscaleInstallHintNamesThePlatform(t *testing.T) {
	cases := map[string]string{
		"linux":   "install.sh",
		"darwin":  "download/mac",
		"windows": "winget",
		"freebsd": "tailscale.com/download",
	}
	for goos, want := range cases {
		t.Run(goos, func(t *testing.T) {
			if hint := tailscaleInstallHint(goos); !strings.Contains(hint, want) {
				t.Errorf("tailscaleInstallHint(%q) = %q, want %q", goos, hint, want)
			}
		})
	}
}
