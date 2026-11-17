package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/amirashouri/tasktracker-server/internal/install"
)

type updateOptions struct {
	version         string
	serverBinaryURL string
	dryRun          bool
}

// runUpdate moves an installed instance to a release the operator names.
//
// There is no "check for updates": tt never phones home, so "latest" is the tag
// or release asset the operator passes. A Docker instance pulls that image tag;
// a native instance downloads that binary over the one the unit supervises and
// restarts it.
func runUpdate(
	ctx context.Context,
	args []string,
	stdout, stderr io.Writer,
	statePath string,
	runner install.Runner,
) int {
	var opts updateOptions
	fs := flag.NewFlagSet("update", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&opts.version, "version", "", "server release to update to: the image tag for a Docker instance, or the version recorded for a native one")
	fs.StringVar(&opts.serverBinaryURL, "server-binary-url", "", "https URL of the server binary for a native instance; tt never phones home, so the operator chooses this URL")
	fs.BoolVar(&opts.dryRun, "dry-run", false, "print the update plan and execute nothing")
	if code, done := parseFlags(fs, args, stderr); done {
		return code
	}

	state, err := loadState(statePath)
	if err != nil {
		fmt.Fprintf(stderr, "tt update: %v\n", err)
		return 1
	}
	if state.Version == 0 {
		fmt.Fprintln(stderr, "tt update: no installation found; run tt install")
		return 1
	}

	steps, err := install.Next(state, install.Command{
		Action:          install.ActionUpdate,
		Version:         strings.TrimSpace(opts.version),
		ServerBinaryURL: strings.TrimSpace(opts.serverBinaryURL),
	})
	if err != nil {
		fmt.Fprintf(stderr, "tt update: %v\n", err)
		return 1
	}

	if opts.dryRun {
		fmt.Fprintf(stdout, "Would update %q:\n", state.InstanceName)
		for _, step := range steps {
			fmt.Fprintf(stdout, "  - %s\n", step.Description)
		}
		if step := fetchStep(steps); step != nil {
			fmt.Fprintf(stdout, "  fetch %s -> %s\n", step.SourceURL, step.State.BinaryPath)
		}
		return 0
	}

	if code := execute(ctx, steps, stderr, runner); code != 0 {
		return code
	}

	final := install.StateFromSteps(steps)
	if state.Runtime == install.RuntimeNative {
		fmt.Fprintf(stdout, "Updated %q. The service is running the new binary.\n", final.InstanceName)
		return 0
	}
	fmt.Fprintf(stdout, "Updated %q to %s.\n", final.InstanceName, install.ImageRef(final.ImageTag))
	// A Docker update rewrites the compose file from the state, and the state
	// only knows the loopback publish; `tt expose` is what widens it, exactly as
	// after a repair.
	fmt.Fprintln(stdout, "If this instance was exposed, re-run `tt expose` to publish the updated server.")
	return 0
}

func fetchStep(steps []install.Step) *install.Step {
	for i := range steps {
		if steps[i].Kind == install.StepFetchBinary {
			return &steps[i]
		}
	}
	return nil
}
