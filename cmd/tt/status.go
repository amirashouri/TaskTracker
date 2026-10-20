package main

import (
	"context"
	"flag"
	"fmt"
	"io"

	"github.com/amirashouri/tasktracker-server/internal/install"
)

// statusDeps is the injectable surface of the two diagnostic commands. They are
// read-only: the checks never write state, start a container, or repair an
// instance behind the operator's back.
type statusDeps struct {
	stdout, stderr io.Writer
	statePath      string
	checks         install.CheckFunc
}

func runStatus(ctx context.Context, args []string, stdout, stderr io.Writer, statePath string) int {
	return runCheckCommand(ctx, "status", args, statusDeps{
		stdout:    stdout,
		stderr:    stderr,
		statePath: statePath,
		checks:    install.Checks,
	}, false)
}

func runDoctor(ctx context.Context, args []string, stdout, stderr io.Writer, statePath string) int {
	return runCheckCommand(ctx, "doctor", args, statusDeps{
		stdout:    stdout,
		stderr:    stderr,
		statePath: statePath,
		checks:    install.Checks,
	}, true)
}

func runCheckCommand(ctx context.Context, command string, args []string, deps statusDeps, doctor bool) int {
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	fs.SetOutput(deps.stderr)
	if code, done := parseFlags(fs, args, deps.stderr); done {
		return code
	}

	state, err := loadState(deps.statePath)
	if err != nil {
		fmt.Fprintf(deps.stderr, "tt %s: %v\n", command, err)
		return 1
	}
	if state.Version == 0 {
		fmt.Fprintf(deps.stderr, "tt %s: no installation found; run tt install\n", command)
		return 1
	}

	printState(deps.stdout, state)
	checks := deps.checks(ctx, state, doctor)
	printChecks(deps.stdout, checks)

	if failed := countFailed(checks); failed > 0 {
		fmt.Fprintf(deps.stderr, "tt %s: %s\n", command, failureSummary(failed))
		return 1
	}
	return 0
}

func printChecks(w io.Writer, checks []install.Check) {
	for _, check := range checks {
		fmt.Fprintf(w, "%-6s %-10s %s\n", "["+string(check.Status)+"]", check.Name, check.Summary)
		if check.Fix != "" {
			fmt.Fprintf(w, "%-6s %-10s %s\n", "", "", check.Fix)
		}
	}
}

func countFailed(checks []install.Check) int {
	failed := 0
	for _, check := range checks {
		if check.Status == install.CheckFail {
			failed++
		}
	}
	return failed
}

func failureSummary(failed int) string {
	if failed == 1 {
		return "1 check failed"
	}
	return fmt.Sprintf("%d checks failed", failed)
}

func runLogs(
	ctx context.Context,
	args []string,
	stdout, stderr io.Writer,
	statePath string,
	runner install.Runner,
) int {
	fs := flag.NewFlagSet("logs", flag.ContinueOnError)
	fs.SetOutput(stderr)
	if code, done := parseFlags(fs, args, stderr); done {
		return code
	}

	state, err := loadState(statePath)
	if err != nil {
		fmt.Fprintf(stderr, "tt logs: %v\n", err)
		return 1
	}
	if state.Version == 0 {
		fmt.Fprintln(stderr, "tt logs: no installation found; run tt install")
		return 1
	}
	steps, err := install.Next(state, install.Command{Action: install.ActionLogs})
	if err != nil {
		fmt.Fprintf(stderr, "tt logs: %v\n", err)
		return 1
	}
	return execute(ctx, steps, stderr, runner)
}
