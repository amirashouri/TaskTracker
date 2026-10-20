// Command tt installs and operates a self-hosted TaskTracker server.
//
// The server is one binary and one database; tt is the second binary that
// makes it installable by someone who did not write it. It owns the install
// state, renders the compose file, and never generates a secret twice.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/amirashouri/tasktracker-server/internal/install"
)

// version is the CLI's build version and the server image tag it pins. The
// release build sets it with -ldflags "-X main.version=<tag>".
var version = "dev"

func main() {
	statePath, err := install.DefaultStatePath()
	if err != nil {
		fmt.Fprintf(os.Stderr, "tt: %v\n", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	runner := install.ExecRunner{Stdout: os.Stdout, Stderr: os.Stderr}
	os.Exit(run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr, statePath, version, runner))
}

// run is the composition root for tests: streams, the state path, the build
// version, and the runner are all injectable.
func run(
	ctx context.Context,
	args []string,
	stdin io.Reader,
	stdout, stderr io.Writer,
	statePath, buildVersion string,
	runner install.Runner,
) int {
	if len(args) == 0 {
		usage(stdout)
		return 2
	}

	switch args[0] {
	case "install":
		return runInstall(ctx, args[1:], stdin, stdout, stderr, statePath, buildVersion, runner)
	case "status":
		return runStatus(ctx, args[1:], stdout, stderr, statePath, runner)
	case "doctor":
		return runDoctor(ctx, args[1:], stdout, stderr, statePath, runner)
	case "logs":
		return runLogs(ctx, args[1:], stdout, stderr, statePath, runner)
	case "uninstall":
		return runUninstall(ctx, args[1:], stdin, stdout, stderr, statePath, runner)
	case "version", "--version", "-version":
		fmt.Fprintln(stdout, buildVersion)
		return 0
	case "help", "--help", "-h":
		usage(stdout)
		return 0
	default:
		fmt.Fprintf(stderr, "tt: unknown command %q\n", args[0])
		usage(stderr)
		return 2
	}
}

func parseFlags(fs *flag.FlagSet, args []string, stderr io.Writer) (int, bool) {
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0, true
		}
		return 2, true
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "%s: unexpected argument %q\n", fs.Name(), fs.Arg(0))
		return 2, true
	}
	return 0, false
}

func execute(ctx context.Context, steps []install.Step, stderr io.Writer, runner install.Runner) int {
	for _, step := range steps {
		if err := runner.Run(ctx, step); err != nil {
			fmt.Fprintf(stderr, "tt: %v\n", err)
			return 1
		}
	}
	return 0
}

func loadState(statePath string) (install.State, error) {
	state, err := install.LoadState(statePath)
	if errors.Is(err, install.ErrStateNotFound) {
		return install.State{Path: statePath}, nil
	}
	if err != nil {
		return install.State{}, err
	}
	return state, nil
}

func printState(w io.Writer, state install.State) {
	fmt.Fprintf(w, "instance: %s\n", state.InstanceName)
	fmt.Fprintf(w, "mode: %s\n", state.Mode)
	fmt.Fprintf(w, "provider: %s\n", state.Provider)
	fmt.Fprintf(w, "image: tasktracker-server:%s\n", state.ImageTag)
	fmt.Fprintf(w, "server: http://127.0.0.1:%d\n", state.ServerPort)
	fmt.Fprintf(w, "compose: %s\n", state.ComposePath)
}

func usage(w io.Writer) {
	fmt.Fprint(w, `tt — install and operate a self-hosted TaskTracker server

Usage:
  tt install [--mode sqlite|postgres] [--provider lan|tailscale|cloudflare]
             [--data-dir PATH] [--name NAME] [--port PORT]
             [--admin-email EMAIL --admin-password PASSWORD]
             [--admin-display-name NAME] [--yes] [--dry-run]
  tt status
  tt doctor
  tt logs
  tt uninstall
  tt version

tt never phones home and owns no infrastructure: a provider is an account the
customer holds, or no provider at all.
`)
}
