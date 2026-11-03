package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/amirashouri/tasktracker-server/internal/runner"
)

func runAgentRun(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("agent run", flag.ContinueOnError)
	fs.SetOutput(stderr)
	providerName := fs.String("provider", "", "echo, openai-compatible or anthropic (default: as paired)")
	model := fs.String("model", "", "model name (default: as paired)")
	baseURL := fs.String("base-url", "", "model endpoint (default: as paired, then the provider's own default)")
	apiKey := fs.String("api-key", "", "provider API key (default: TT_AGENT_API_KEY, then the config)")
	workspace := fs.String("workspace", ".", "directory the agent may read and edit")
	maxSteps := fs.Int("max-steps", 25, "model turns before the run is failed")
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

	provider, err := runner.NewProvider(runner.ModelConfig{
		Provider:  firstNonEmpty(*providerName, cfg.Provider),
		Model:     firstNonEmpty(*model, cfg.Model),
		BaseURL:   firstNonEmpty(*baseURL, cfg.BaseURL),
		APIKey:    firstNonEmpty(*apiKey, os.Getenv("TT_AGENT_API_KEY"), cfg.APIKey),
		Workspace: *workspace,
		MaxSteps:  *maxSteps,
	})
	if err != nil {
		// A configuration refusal is the user's input, not a server failure.
		fmt.Fprintf(stderr, "tt agent run: %v\n", err)
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

// firstNonEmpty is the flag → environment → config precedence, in one place so a
// later flag cannot quietly skip a source the others honour.
func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
