package main

import (
	"bytes"
	"context"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/amirashouri/tasktracker-server/internal/install"
)

// scriptedRunner returns one error per call, then succeeds. It lets a test
// fail the first `compose up` while leaving the rollback's `compose up` green.
type scriptedRunner struct {
	steps []install.Step
	errs  []error
}

func (r *scriptedRunner) Run(_ context.Context, step install.Step) error {
	r.steps = append(r.steps, step)
	if len(r.errs) == 0 {
		return nil
	}
	err := r.errs[0]
	r.errs = r.errs[1:]
	return err
}

func runExposeForTest(
	t *testing.T,
	statePath string,
	runner install.Runner,
	lanAddress string,
	verify func(context.Context, string) error,
	args ...string,
) (int, string, string) {
	t.Helper()

	var stdout, stderr bytes.Buffer
	code := runExposeWithDeps(context.Background(), args, exposeDeps{
		stdout:     &stdout,
		stderr:     &stderr,
		statePath:  statePath,
		runner:     runner,
		lanAddress: func() string { return lanAddress },
		verify:     verify,
	})
	return code, stdout.String(), stderr.String()
}

func TestExposePublishesTheLANAddressAndPrintsThePairingLink(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "install.json")
	seedState(t, statePath)
	runner := &recordingRunner{}
	var verified string

	code, stdout, stderr := runExposeForTest(t, statePath, runner, "192.168.1.20",
		func(_ context.Context, address string) error {
			verified = address
			return nil
		})

	if code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, stderr)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want no output", stderr)
	}
	if verified != "http://192.168.1.20:8080/readyz" {
		t.Errorf("verified %q, want the LAN address, not loopback", verified)
	}

	if len(runner.steps) != 2 {
		t.Fatalf("ran %d steps, want write-compose and compose-up", len(runner.steps))
	}
	if runner.steps[0].Kind != install.StepWriteCompose {
		t.Errorf("step 0 = %q, want write-compose", runner.steps[0].Kind)
	}
	if runner.steps[1].Kind != install.StepComposeUp {
		t.Errorf("step 1 = %q, want compose-up", runner.steps[1].Kind)
	}
	compose := runner.steps[0].Compose
	if !strings.Contains(compose, `"0.0.0.0:8080:8080"`) {
		t.Errorf("compose does not publish on every interface:\n%s", compose)
	}
	if strings.Contains(compose, `"127.0.0.1:8080:8080"`) {
		t.Errorf("compose still publishes on loopback:\n%s", compose)
	}
	if !strings.Contains(compose, `SERVER_BIND: "0.0.0.0"`) {
		t.Errorf("compose does not bind the container on every interface:\n%s", compose)
	}

	if !strings.Contains(stdout, "Exposed on the LAN: http://192.168.1.20:8080") {
		t.Errorf("stdout does not name the LAN URL:\n%s", stdout)
	}
	wantLink := connectLinkPrefix + url.QueryEscape("http://192.168.1.20:8080")
	if !strings.Contains(stdout, "Pairing link: "+wantLink) {
		t.Errorf("stdout has no pairing link %q:\n%s", wantLink, stdout)
	}
	if strings.Contains(stdout, "no verified server URL") {
		t.Errorf("stdout printed the unverified hint:\n%s", stdout)
	}
}

func TestExposeRequiresAnInstall(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "install.json")
	runner := &recordingRunner{}

	code, _, stderr := runExposeForTest(t, statePath, runner, "192.168.1.20", nil)

	if code != 1 {
		t.Errorf("exit = %d, want 1", code)
	}
	if !strings.Contains(stderr, "run `tt install`") {
		t.Errorf("stderr = %q, want a pointer to tt install", stderr)
	}
	if len(runner.steps) != 0 {
		t.Errorf("ran %d steps without an install", len(runner.steps))
	}
}

func TestExposeRefusesCloudflareUntilT910(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "install.json")
	seedState(t, statePath)
	runner := &recordingRunner{}

	code, _, stderr := runExposeForTest(t, statePath, runner, "192.168.1.20", nil, "--provider=cloudflare")

	if code != 1 {
		t.Errorf("exit = %d, want 1", code)
	}
	if !strings.Contains(stderr, "not implemented") || !strings.Contains(stderr, "T9.10") {
		t.Errorf("stderr = %q, want the later-provider message", stderr)
	}
	if len(runner.steps) != 0 {
		t.Errorf("cloudflare ran %d steps, want none", len(runner.steps))
	}
}

func TestExposeRefusesAnUnknownProvider(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "install.json")
	seedState(t, statePath)
	runner := &recordingRunner{}

	code, _, stderr := runExposeForTest(t, statePath, runner, "192.168.1.20", nil, "--provider=bogus")

	if code != 1 {
		t.Errorf("exit = %d, want 1", code)
	}
	if !strings.Contains(stderr, "not lan, tailscale, or cloudflare") {
		t.Errorf("stderr = %q, want the provider list", stderr)
	}
	if len(runner.steps) != 0 {
		t.Errorf("ran %d steps for an unknown provider", len(runner.steps))
	}
}

// The install state is the instance's provider choice. The LAN default must
// not override a tailscale/cloudflare state just because those flags are not
// implemented yet.
func TestExposeRefusesAProviderMismatch(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "install.json")
	state := seedState(t, statePath)
	state.Provider = install.ProviderTailscale
	if err := install.SaveState(state); err != nil {
		t.Fatalf("save tailscale state: %v", err)
	}
	runner := &recordingRunner{}

	code, _, stderr := runExposeForTest(t, statePath, runner, "192.168.1.20", nil)

	if code != 1 {
		t.Errorf("exit = %d, want 1", code)
	}
	if !strings.Contains(stderr, "configured for provider tailscale") {
		t.Errorf("stderr = %q, want the provider mismatch", stderr)
	}
	if len(runner.steps) != 0 {
		t.Errorf("ran %d steps for a provider mismatch", len(runner.steps))
	}
}

func TestExposeRefusesWhenThereIsNoLANAddress(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "install.json")
	seedState(t, statePath)
	runner := &recordingRunner{}

	code, _, stderr := runExposeForTest(t, statePath, runner, "", nil)

	if code != 1 {
		t.Errorf("exit = %d, want 1", code)
	}
	if !strings.Contains(stderr, "no LAN address") {
		t.Errorf("stderr = %q, want the missing-address message", stderr)
	}
	if len(runner.steps) != 0 {
		t.Errorf("ran %d steps without a LAN address", len(runner.steps))
	}
}

// A detector bug must not turn into a loopback pairing link. The command
// checks the address it was given, not just the detector's promise.
func TestExposeRefusesANonLANAddress(t *testing.T) {
	for _, address := range []string{"127.0.0.1", "0.0.0.0"} {
		t.Run(address, func(t *testing.T) {
			statePath := filepath.Join(t.TempDir(), "install.json")
			seedState(t, statePath)
			runner := &recordingRunner{}

			code, _, stderr := runExposeForTest(t, statePath, runner, address, nil)

			if code != 1 {
				t.Errorf("exit = %d, want 1", code)
			}
			if !strings.Contains(stderr, "not a LAN address") {
				t.Errorf("stderr = %q, want the non-LAN refusal", stderr)
			}
			if len(runner.steps) != 0 {
				t.Errorf("ran %d steps for address %s", len(runner.steps), address)
			}
		})
	}
}

func TestExposeRestoresThePreviousComposeWhenVerificationFails(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "install.json")
	state := seedState(t, statePath)
	loopback, err := install.RenderCompose(state)
	if err != nil {
		t.Fatalf("RenderCompose: %v", err)
	}
	if err := os.WriteFile(state.ComposePath, []byte(loopback), 0o600); err != nil {
		t.Fatalf("write loopback compose: %v", err)
	}

	runner := &recordingRunner{}
	code, stdout, stderr := runExposeForTest(t, statePath, runner, "192.168.1.20",
		func(context.Context, string) error { return errors.New("connection refused") })

	if code != 1 {
		t.Errorf("exit = %d, want 1", code)
	}
	if len(runner.steps) != 4 {
		t.Fatalf("ran %d steps, want publish and restore", len(runner.steps))
	}
	if !strings.Contains(runner.steps[0].Compose, `"0.0.0.0:8080:8080"`) {
		t.Error("the first write was not the exposed compose")
	}
	if runner.steps[2].Kind != install.StepWriteCompose || runner.steps[2].Compose != loopback {
		t.Error("the previous compose was not restored")
	}
	if runner.steps[3].Kind != install.StepComposeUp {
		t.Error("the restored compose was not applied")
	}
	if strings.Contains(stdout, "Pairing link:") {
		t.Errorf("stdout printed a pairing link after a failed verification:\n%s", stdout)
	}
	if !strings.Contains(stderr, "http://192.168.1.20:8080/readyz") {
		t.Errorf("stderr = %q, want the verified URL", stderr)
	}
	if !strings.Contains(stderr, "restored the previous compose") {
		t.Errorf("stderr = %q, want the restore result", stderr)
	}
}

func TestExposeRestoresThePreviousComposeWhenComposeUpFails(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "install.json")
	state := seedState(t, statePath)
	loopback, err := install.RenderCompose(state)
	if err != nil {
		t.Fatalf("RenderCompose: %v", err)
	}
	if err := os.WriteFile(state.ComposePath, []byte(loopback), 0o600); err != nil {
		t.Fatalf("write loopback compose: %v", err)
	}

	runner := &scriptedRunner{errs: []error{
		nil,
		errors.New("docker compose up: exit 1"),
		nil,
		nil,
	}}
	code, _, stderr := runExposeForTest(t, statePath, runner, "192.168.1.20",
		func(context.Context, string) error {
			t.Error("verification ran after compose up failed")
			return nil
		})

	if code != 1 {
		t.Errorf("exit = %d, want 1", code)
	}
	if len(runner.steps) != 4 {
		t.Fatalf("ran %d steps, want publish and restore", len(runner.steps))
	}
	if runner.steps[2].Kind != install.StepWriteCompose || runner.steps[2].Compose != loopback {
		t.Error("the previous compose was not restored")
	}
	if !strings.Contains(stderr, "docker compose up: exit 1") {
		t.Errorf("stderr = %q, want the compose error", stderr)
	}
	if !strings.Contains(stderr, "restored the previous compose") {
		t.Errorf("stderr = %q, want the restore result", stderr)
	}
}

func TestExposeRefusesACorruptState(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "install.json")
	if err := os.WriteFile(statePath, []byte(`{"version":1,`), 0o600); err != nil {
		t.Fatalf("write corrupt state: %v", err)
	}
	runner := &recordingRunner{}

	code, _, stderr := runExposeForTest(t, statePath, runner, "192.168.1.20", nil)

	if code != 1 {
		t.Errorf("exit = %d, want 1", code)
	}
	if !strings.Contains(stderr, "not valid") {
		t.Errorf("stderr = %q, want the invalid-state message", stderr)
	}
	if len(runner.steps) != 0 {
		t.Errorf("ran %d steps for a corrupt state", len(runner.steps))
	}
}

func TestExposeIsDispatched(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "install.json")
	runner := &recordingRunner{}

	code, _, stderr := runCLI(t, []string{"expose"}, "", statePath, "v1.2.3", runner)

	if code != 1 {
		t.Errorf("exit = %d, want 1", code)
	}
	if !strings.Contains(stderr, "run `tt install`") {
		t.Errorf("stderr = %q, want the dispatch to reach runExpose", stderr)
	}
}
