package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/amirashouri/tasktracker-server/internal/install"
)

const exposeReadyTimeout = 2 * time.Minute

type exposeDeps struct {
	stdout, stderr io.Writer
	statePath      string
	runner         install.Runner
	lanAddress     func() string
	verify         func(context.Context, string) error
}

func runExpose(
	ctx context.Context,
	args []string,
	stdout, stderr io.Writer,
	statePath string,
	runner install.Runner,
) int {
	return runExposeWithDeps(ctx, args, exposeDeps{
		stdout:     stdout,
		stderr:     stderr,
		statePath:  statePath,
		runner:     runner,
		lanAddress: install.LANAddress,
		verify: func(ctx context.Context, address string) error {
			return install.WaitReady(ctx, address, exposeReadyTimeout, time.Second)
		},
	})
}

// runExposeWithDeps is the testable command. The LAN detector and the HTTP
// verifier are injected so a test can prove the address it dials without
// depending on the developer's network or a Docker daemon.
func runExposeWithDeps(ctx context.Context, args []string, deps exposeDeps) int {
	opts := struct {
		provider string
	}{provider: string(install.ProviderLAN)}

	fs := flag.NewFlagSet("expose", flag.ContinueOnError)
	fs.SetOutput(deps.stderr)
	fs.StringVar(&opts.provider, "provider", opts.provider, "exposure provider: lan (default); tailscale and cloudflare arrive later")
	if code, done := parseFlags(fs, args, deps.stderr); done {
		return code
	}

	provider := install.Provider(opts.provider)
	switch provider {
	case install.ProviderLAN:
	case install.ProviderTailscale, install.ProviderCloudflare:
		fmt.Fprintf(
			deps.stderr,
			"tt expose: provider %s is not implemented in this build; use --provider=lan (tailscale and cloudflare arrive in T9.9/T9.10).\n",
			provider,
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
		deps.verify = func(ctx context.Context, address string) error {
			return install.WaitReady(ctx, address, exposeReadyTimeout, time.Second)
		}
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
	// not quietly do it either. A tailscale/cloudflare install stays local
	// until those providers have an implementation.
	if state.Provider != provider {
		fmt.Fprintf(
			deps.stderr,
			"tt expose: this install is configured for provider %s; refusing to publish it with provider %s. Provider %s exposure arrives in T9.9/T9.10.\n",
			state.Provider, provider, state.Provider,
		)
		return 1
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
