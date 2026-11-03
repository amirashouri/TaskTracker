package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/amirashouri/tasktracker-server/internal/runner"
)

func runAgentRun(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("agent run", flag.ContinueOnError)
	fs.SetOutput(stderr)
	providerName := fs.String("provider", "", "provider to run (default: the one this machine was paired with)")
	once := fs.Bool("once", false, "claim at most one run, then exit")
	wait := fs.Int("wait", 25, "seconds to wait for a run before answering idle")
	heartbeat := fs.Duration("heartbeat", 20*time.Second, "how often to renew the run's lease")
	if code, done := parseFlags(fs, args, stderr); done {
		return code
	}

	configPath, err := runner.DefaultConfigPath()
	if err != nil {
		return fail(ctx, stderr, "tt agent run", err)
	}
	cfg, err := runner.LoadConfig(configPath)
	if errors.Is(err, runner.ErrConfigNotFound) {
		fmt.Fprintln(stderr, "tt agent run: not connected; run tt agent connect")
		return 1
	}
	if err != nil {
		return fail(ctx, stderr, "tt agent run", err)
	}

	name := *providerName
	if name == "" {
		name = cfg.Provider
	}
	provider, ok := runner.ProviderByName(name)
	if !ok {
		fmt.Fprintf(stderr, "tt agent run: unknown provider %q; this build supports echo\n", name)
		return 2
	}

	client := runner.NewClient(cfg.ServerURL, cfg.Token)
	worker := runner.NewWorker(client, provider)
	worker.WaitSeconds = *wait
	worker.HeartbeatInterval = *heartbeat
	worker.Log = func(format string, values ...any) {
		fmt.Fprintf(stdout, "tt agent: "+format+"\n", values...)
	}

	fmt.Fprintf(stdout, "tt agent: runner %s on %s (provider %s)\n", cfg.RunnerName, cfg.ServerURL, provider.Name())
	if err := worker.Run(ctx, *once); err != nil {
		return fail(ctx, stderr, "tt agent run", err)
	}
	fmt.Fprintln(stdout, "tt agent: stopped")
	return 0
}
