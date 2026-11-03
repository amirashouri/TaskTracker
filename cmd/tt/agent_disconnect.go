package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/amirashouri/tasktracker-server/internal/runner"
)

// runAgentDisconnect retires this machine server-side, then removes the local
// credential.
//
// The order matters. Deleting the config first would leave a live token on the
// server that nothing on this machine can revoke — a stolen copy of agent.json
// would keep working. If the server cannot be reached, the config is kept so the
// command can be retried; only "this token is already gone" is treated as done.
func runAgentDisconnect(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("agent disconnect", flag.ContinueOnError)
	fs.SetOutput(stderr)
	if code, done := parseFlags(fs, args, stderr); done {
		return code
	}

	configPath, err := runner.DefaultConfigPath()
	if err != nil {
		return fail(ctx, stderr, "tt agent disconnect", err)
	}
	cfg, err := runner.LoadConfig(configPath)
	if errors.Is(err, runner.ErrConfigNotFound) {
		fmt.Fprintln(stderr, "tt agent disconnect: not connected")
		return 1
	}
	if err != nil {
		return fail(ctx, stderr, "tt agent disconnect", err)
	}

	client := runner.NewClient(cfg.ServerURL, cfg.Token)
	if err := client.RevokeSelf(ctx); err != nil {
		if !runner.IsUnauthorized(err) {
			fmt.Fprintf(stderr, "tt agent disconnect: revoke: %v\n", err)
			fmt.Fprintln(stderr, "the local config was kept so this can be retried")
			return 1
		}
		fmt.Fprintln(stdout, "server did not recognise this token; removing the local config anyway")
	}

	if err := runner.RemoveConfig(configPath); err != nil {
		return fail(ctx, stderr, "tt agent disconnect", err)
	}
	fmt.Fprintf(stdout, "disconnected %s; local config removed\n", cfg.RunnerName)
	return 0
}
