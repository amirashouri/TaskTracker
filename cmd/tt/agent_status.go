package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/amirashouri/tasktracker-server/internal/runner"
)

// runAgentStatus reports this machine's pairing and whether the server answers.
//
// It never prints the token, not even a suffix: the status output is the kind of
// thing that ends up pasted into an issue.
func runAgentStatus(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("agent status", flag.ContinueOnError)
	fs.SetOutput(stderr)
	if code, done := parseFlags(fs, args, stderr); done {
		return code
	}

	configPath, err := runner.DefaultConfigPath()
	if err != nil {
		return fail(ctx, stderr, "tt agent status", err)
	}
	cfg, err := runner.LoadConfig(configPath)
	if errors.Is(err, runner.ErrConfigNotFound) {
		fmt.Fprintf(stderr, "tt agent status: not connected; run tt agent connect\n")
		return 1
	}
	if err != nil {
		return fail(ctx, stderr, "tt agent status", err)
	}

	fmt.Fprintf(stdout, "server: %s\n", cfg.ServerURL)
	fmt.Fprintf(stdout, "runner: %s (%s)\n", cfg.RunnerName, cfg.RunnerID)
	fmt.Fprintf(stdout, "provider: %s\n", cfg.Provider)
	if cfg.Model != "" {
		fmt.Fprintf(stdout, "model: %s\n", cfg.Model)
	}
	fmt.Fprintf(stdout, "config: %s\n", configPath)
	fmt.Fprintln(stdout, "token: stored")

	if err := runner.Probe(ctx, cfg.ServerURL); err != nil {
		fmt.Fprintf(stdout, "reachable: no (%v)\n", err)
		return 1
	}
	fmt.Fprintln(stdout, "reachable: yes")
	return 0
}
