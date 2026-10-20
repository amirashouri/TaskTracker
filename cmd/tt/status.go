package main

import (
	"context"
	"flag"
	"fmt"
	"io"

	"github.com/amirashouri/tasktracker-server/internal/install"
)

func runStatus(
	ctx context.Context,
	args []string,
	stdout, stderr io.Writer,
	statePath string,
	runner install.Runner,
) int {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	fs.SetOutput(stderr)
	if code, done := parseFlags(fs, args, stderr); done {
		return code
	}

	state, err := loadState(statePath)
	if err != nil {
		fmt.Fprintf(stderr, "tt status: %v\n", err)
		return 1
	}
	if state.Version == 0 {
		fmt.Fprintln(stderr, "tt status: no installation found; run tt install")
		return 1
	}
	printState(stdout, state)

	steps, err := install.Next(state, install.Command{Action: install.ActionStatus})
	if err != nil {
		fmt.Fprintf(stderr, "tt status: %v\n", err)
		return 1
	}
	return execute(ctx, steps, stderr, runner)
}

func runDoctor(
	ctx context.Context,
	args []string,
	stdout, stderr io.Writer,
	statePath string,
	runner install.Runner,
) int {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	fs.SetOutput(stderr)
	if code, done := parseFlags(fs, args, stderr); done {
		return code
	}

	state, err := loadState(statePath)
	if err != nil {
		fmt.Fprintf(stderr, "tt doctor: %v\n", err)
		return 1
	}
	if state.Version == 0 {
		fmt.Fprintln(stderr, "tt doctor: no installation found; run tt install")
		return 1
	}
	printState(stdout, state)

	steps, err := install.Next(state, install.Command{Action: install.ActionDoctor})
	if err != nil {
		fmt.Fprintf(stderr, "tt doctor: %v\n", err)
		return 1
	}
	return execute(ctx, steps, stderr, runner)
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
