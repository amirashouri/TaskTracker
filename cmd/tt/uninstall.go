package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/amirashouri/tasktracker-server/internal/install"
)

func runUninstall(
	ctx context.Context,
	args []string,
	stdin io.Reader,
	stdout, stderr io.Writer,
	statePath string,
	runner install.Runner,
) int {
	fs := flag.NewFlagSet("uninstall", flag.ContinueOnError)
	fs.SetOutput(stderr)
	if code, done := parseFlags(fs, args, stderr); done {
		return code
	}

	state, err := loadState(statePath)
	if err != nil {
		fmt.Fprintf(stderr, "tt uninstall: %v\n", err)
		return 1
	}
	if state.Version == 0 {
		fmt.Fprintln(stderr, "tt uninstall: no installation found; nothing to remove")
		return 1
	}

	fmt.Fprintf(stdout, "Type the instance name %q to uninstall: ", state.InstanceName)
	typed, err := bufio.NewReader(stdin).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		fmt.Fprintf(stderr, "\ntt uninstall: read instance name: %v\n", err)
		return 1
	}
	typed = strings.TrimSpace(typed)

	steps, err := install.Next(state, install.Command{
		Action:      install.ActionUninstall,
		ConfirmName: typed,
	})
	if err != nil {
		fmt.Fprintf(stderr, "\ntt uninstall: %v\n", err)
		return 1
	}
	if code := execute(ctx, steps, stderr, runner); code != 0 {
		return code
	}

	fmt.Fprintf(stdout, "Removed %q. Data volumes were left in place.\n", state.InstanceName)
	return 0
}
