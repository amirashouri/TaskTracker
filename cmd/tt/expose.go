package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/amirashouri/tasktracker-server/internal/install"
)

const exposeReadyTimeout = 2 * time.Minute

func waitReady(ctx context.Context, address string) error {
	return install.WaitReady(ctx, address, exposeReadyTimeout, time.Second)
}

type exposeDeps struct {
	stdout, stderr io.Writer
	statePath      string
	runner         install.Runner
	lanAddress     func() string
	verify         func(context.Context, string) error
	lookTailscale  func() (string, error)
	runTailscale   func(context.Context, ...string) (string, error)
	goos           string
}

func runExpose(
	ctx context.Context,
	args []string,
	stdout, stderr io.Writer,
	statePath string,
	runner install.Runner,
) int {
	return runExposeWithDeps(ctx, args, exposeDeps{
		stdout:        stdout,
		stderr:        stderr,
		statePath:     statePath,
		runner:        runner,
		lanAddress:    install.LANAddress,
		verify:        waitReady,
		lookTailscale: func() (string, error) { return exec.LookPath("tailscale") },
		runTailscale:  runTailscaleCommand,
		goos:          runtime.GOOS,
	})
}

// runExposeWithDeps is the testable command. The LAN detector and the HTTP
// verifier are injected so a test can prove the address it dials without
// depending on the developer's network or a Docker daemon.
func runExposeWithDeps(ctx context.Context, args []string, deps exposeDeps) int {
	opts := struct {
		provider    string
		tailnetOnly bool
	}{provider: string(install.ProviderLAN)}

	fs := flag.NewFlagSet("expose", flag.ContinueOnError)
	fs.SetOutput(deps.stderr)
	fs.StringVar(&opts.provider, "provider", opts.provider, "exposure provider: lan (default) or tailscale; cloudflare arrives in T9.10")
	fs.BoolVar(&opts.tailnetOnly, "tailnet-only", false, "use Tailscale Serve (tailnet only) instead of public Funnel")
	if code, done := parseFlags(fs, args, deps.stderr); done {
		return code
	}

	provider := install.Provider(opts.provider)
	switch provider {
	case install.ProviderLAN, install.ProviderTailscale:
	case install.ProviderCloudflare:
		fmt.Fprintln(
			deps.stderr,
			"tt expose: provider cloudflare is not implemented in this build; it arrives in T9.10. Use --provider=lan or --provider=tailscale.",
		)
		return 1
	default:
		fmt.Fprintf(deps.stderr, "tt expose: provider %q is not lan, tailscale, or cloudflare\n", opts.provider)
		return 1
	}

	if deps.lanAddress == nil {
		deps.lanAddress = install.LANAddress
	}
	if deps.verify == nil {
		deps.verify = waitReady
	}
	if deps.lookTailscale == nil {
		deps.lookTailscale = func() (string, error) { return exec.LookPath("tailscale") }
	}
	if deps.runTailscale == nil {
		deps.runTailscale = runTailscaleCommand
	}
	if deps.goos == "" {
		deps.goos = runtime.GOOS
	}

	state, err := loadState(deps.statePath)
	if err != nil {
		fmt.Fprintf(deps.stderr, "tt expose: %v\n", err)
		return 1
	}
	if state.Version == 0 {
		fmt.Fprintln(deps.stderr, "tt expose: no installation found; run `tt install` first")
		return 1
	}
	// `tt install` refuses to change an instance's provider; `tt expose` must
	// not quietly do it either. The state is the instance's provider choice.
	if state.Provider != provider {
		fmt.Fprintf(
			deps.stderr,
			"tt expose: this install is configured for provider %s; refusing to publish it with provider %s. Run `tt expose --provider=%s` instead, or reinstall with --provider=%s (tt install cannot change a provider in place).\n",
			state.Provider, provider, state.Provider, provider,
		)
		return 1
	}

	if provider == install.ProviderTailscale {
		return exposeTailscale(ctx, deps, state, opts.tailnetOnly)
	}

	ip := strings.TrimSpace(deps.lanAddress())
	if ip == "" {
		fmt.Fprintln(deps.stderr, "tt expose: no LAN address detected; connect this host to the network and try again")
		return 1
	}
	parsed := net.ParseIP(ip)
	if parsed == nil || parsed.IsLoopback() || parsed.IsUnspecified() {
		fmt.Fprintf(deps.stderr, "tt expose: detected address %q is not a LAN address; refusing to publish on loopback\n", ip)
		return 1
	}

	hostPort := net.JoinHostPort(ip, strconv.Itoa(state.ServerPort))
	serverURL := "http://" + hostPort
	readyURL := serverURL + "/readyz"

	compose, err := install.RenderExposedCompose(state)
	if err != nil {
		fmt.Fprintf(deps.stderr, "tt expose: %v\n", err)
		return 1
	}

	// Keep the bytes that were there before this command. A failed recreate or
	// verification restores them, so a broken expose does not leave the server
	// published on every interface.
	previous, err := os.ReadFile(state.ComposePath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		fmt.Fprintf(deps.stderr, "tt expose: read %s: %v\n", state.ComposePath, err)
		return 1
	}

	if err := applyCompose(ctx, deps, state, compose, "Recreate the server with the LAN publish"); err != nil {
		reportRestore(ctx, deps, state, previous, err)
		return 1
	}

	if err := deps.verify(ctx, readyURL); err != nil {
		reportRestore(ctx, deps, state, previous, fmt.Errorf("no response from %s: %w", readyURL, err))
		return 1
	}

	fmt.Fprintf(deps.stdout, "Exposed on the LAN: %s\n", serverURL)
	fmt.Fprintln(deps.stdout, "Plain HTTP on your local network: trust this network, and do not forward this port from the internet.")
	fmt.Fprintln(deps.stdout, "`tt install` rewrites the compose file to loopback; re-run `tt expose` after a repair.")
	printPairing(deps.stdout, provider, serverURL)
	return 0
}

// exposeTailscale keeps the server on its loopback publish and lets Tailscale
// terminate TLS in front of it. Nothing is printed as a pairing target until
// the HTTPS name has answered /readyz.
func exposeTailscale(ctx context.Context, deps exposeDeps, state install.State, tailnetOnly bool) int {
	if _, err := deps.lookTailscale(); err != nil {
		fmt.Fprintf(deps.stderr, "tt expose: Tailscale is not installed or not on PATH. %s\n", tailscaleInstallHint(deps.goos))
		return 1
	}

	statusOutput, err := deps.runTailscale(ctx, "status", "--json")
	if err != nil {
		fmt.Fprintf(deps.stderr, "tt expose: cannot read Tailscale status: %v\n", err)
		return 1
	}
	status, err := parseTailscaleStatus(statusOutput)
	if err != nil {
		fmt.Fprintf(deps.stderr, "tt expose: %v\n", err)
		return 1
	}
	if status.BackendState != "Running" {
		fmt.Fprintf(
			deps.stderr,
			"tt expose: Tailscale is not connected to a tailnet (state %q). Run `tailscale up` and finish sign-in, then re-run.\n",
			status.BackendState,
		)
		return 1
	}
	if len(status.CertDomains) == 0 {
		fmt.Fprintln(
			deps.stderr,
			"tt expose: Tailscale HTTPS is not enabled for this tailnet. In the Tailscale admin console, open DNS and enable MagicDNS and HTTPS certificates; these two dashboard steps cannot be automated without an API token. Then re-run.",
		)
		return 1
	}
	name := status.httpsName()
	if name == "" {
		fmt.Fprintln(
			deps.stderr,
			"tt expose: Tailscale returned no usable HTTPS name. Enable MagicDNS and HTTPS certificates for this node in the Tailscale admin console, then re-run.",
		)
		return 1
	}

	compose, err := install.RenderCompose(state)
	if err != nil {
		fmt.Fprintf(deps.stderr, "tt expose: %v\n", err)
		return 1
	}

	// The compose still publishes on loopback. Keep the previous bytes so a
	// failed recreate cannot leave the stack half-applied.
	previous, err := os.ReadFile(state.ComposePath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		fmt.Fprintf(deps.stderr, "tt expose: read %s: %v\n", state.ComposePath, err)
		return 1
	}
	if err := applyCompose(ctx, deps, state, compose, "Apply the Tailscale trusted-proxy setting"); err != nil {
		reportRestore(ctx, deps, state, previous, err)
		return 1
	}

	args := tailscaleExposeArgs(tailnetOnly, state.ServerPort)
	if _, err := deps.runTailscale(ctx, args...); err != nil {
		fmt.Fprintf(deps.stderr, "tt expose: `tailscale %s` failed: %v\n", strings.Join(args, " "), err)
		if !tailnetOnly {
			fmt.Fprintln(deps.stderr, "Funnel must be enabled for this node in the Tailscale admin console; enable it and re-run.")
		}
		return 1
	}

	serverURL := "https://" + name
	readyURL := serverURL + "/readyz"
	if err := deps.verify(ctx, readyURL); err != nil {
		fmt.Fprintf(deps.stderr, "tt expose: no response from %s: %v\n", readyURL, err)
		fmt.Fprintf(
			deps.stderr,
			"The Tailscale %s configuration is still active; run `%s` to remove it and try again.\n",
			tailscaleModeName(tailnetOnly), tailscaleResetCommand(tailnetOnly),
		)
		return 1
	}

	if tailnetOnly {
		fmt.Fprintf(deps.stdout, "Exposed to your tailnet with Tailscale Serve: %s\n", serverURL)
	} else {
		fmt.Fprintf(deps.stdout, "Exposed with Tailscale Funnel: %s\n", serverURL)
	}
	fmt.Fprintln(deps.stdout, "The server stays bound to loopback; Tailscale reaches it from 127.0.0.1 over HTTPS.")
	fmt.Fprintf(deps.stdout, "To stop exposing: %s\n", tailscaleResetCommand(tailnetOnly))
	printPairing(deps.stdout, install.ProviderTailscale, serverURL)
	return 0
}

func applyCompose(ctx context.Context, deps exposeDeps, state install.State, compose, description string) error {
	steps := []install.Step{
		{Kind: install.StepWriteCompose, Description: "Write the compose file", State: state, Compose: compose},
		{Kind: install.StepComposeUp, Description: description, State: state},
	}
	for _, step := range steps {
		if err := deps.runner.Run(ctx, step); err != nil {
			return err
		}
	}
	return nil
}

// restoreCompose writes the bytes read before the change and recreates the
// stack. If the compose file did not exist, the loopback render is the safe
// fallback rather than an empty file.
func restoreCompose(ctx context.Context, deps exposeDeps, state install.State, previous []byte) error {
	compose := string(previous)
	if compose == "" {
		rendered, err := install.RenderCompose(state)
		if err != nil {
			return err
		}
		compose = rendered
	}
	return applyCompose(ctx, deps, state, compose, "Restore the previous compose")
}

func reportRestore(ctx context.Context, deps exposeDeps, state install.State, previous []byte, cause error) {
	if err := restoreCompose(ctx, deps, state, previous); err != nil {
		fmt.Fprintf(deps.stderr, "tt expose: %v; restoring the previous compose also failed: %v\n", cause, err)
		return
	}
	fmt.Fprintf(deps.stderr, "tt expose: %v; restored the previous compose\n", cause)
}
