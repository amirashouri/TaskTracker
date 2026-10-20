package main

import (
	"context"
	"flag"
	"fmt"
	"io"

	"github.com/amirashouri/tasktracker-server/internal/install"
)

func runInstall(
	ctx context.Context,
	args []string,
	stdout, stderr io.Writer,
	statePath, buildVersion string,
	runner install.Runner,
) int {
	fs := flag.NewFlagSet("install", flag.ContinueOnError)
	fs.SetOutput(stderr)
	mode := fs.String("mode", string(install.ModeSQLite), "database mode: sqlite or postgres")
	provider := fs.String("provider", string(install.ProviderLAN), "exposure provider: lan, tailscale, or cloudflare")
	dataDir := fs.String("data-dir", install.DefaultDataDir(), "directory for the rendered compose file")
	name := fs.String("name", "tasktracker", "instance name")
	port := fs.Int("port", 8080, "host port for the HTTP server")
	dryRun := fs.Bool("dry-run", false, "print the rendered compose and execute nothing")
	if code, done := parseFlags(fs, args, stderr); done {
		return code
	}

	explicit := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { explicit[f.Name] = true })

	state, err := loadState(statePath)
	if err != nil {
		fmt.Fprintf(stderr, "tt install: %v\n", err)
		return 1
	}
	wasInstalled := state.Version != 0

	// A repair with no flags must reproduce the instance it finds, not the
	// CLI's new-install defaults. An explicit flag that changes the shape is
	// left for sameInstance to reject with a real explanation.
	if wasInstalled {
		if !explicit["mode"] {
			*mode = string(state.Mode)
		}
		if !explicit["provider"] {
			*provider = string(state.Provider)
		}
		if !explicit["data-dir"] {
			*dataDir = state.DataDir
		}
		if !explicit["name"] {
			*name = state.InstanceName
		}
		if !explicit["port"] {
			*port = state.ServerPort
		}
	}

	cmd := install.Command{
		Action:       install.ActionInstall,
		Mode:         install.Mode(*mode),
		Provider:     install.Provider(*provider),
		DataDir:      *dataDir,
		InstanceName: *name,
		Version:      buildVersion,
		ServerPort:   *port,
	}

	// A secret is generated only when there is no state to preserve. The
	// plan ignores this field on a repair, and the test below pins that.
	if !wasInstalled {
		secret, err := install.NewSecret()
		if err != nil {
			fmt.Fprintf(stderr, "tt install: %v\n", err)
			return 1
		}
		cmd.Secret = secret
		if cmd.Mode == install.ModePostgres {
			password, err := install.NewSecret()
			if err != nil {
				fmt.Fprintf(stderr, "tt install: %v\n", err)
				return 1
			}
			cmd.DBPassword = password
		}
	}

	steps, err := install.Next(state, cmd)
	if err != nil {
		fmt.Fprintf(stderr, "tt install: %v\n", err)
		return 1
	}

	if *dryRun {
		compose := install.ComposeFromSteps(steps)
		if compose == "" {
			fmt.Fprintln(stderr, "tt install: the plan produced no compose file")
			return 1
		}
		fmt.Fprint(stdout, compose)
		return 0
	}

	for _, step := range steps {
		if err := runner.Run(ctx, step); err != nil {
			fmt.Fprintf(stderr, "tt install: %v\n", err)
			return 1
		}
	}

	final := install.StateFromSteps(steps)
	if !wasInstalled {
		fmt.Fprintf(stdout, "JWT_SECRET=%s\n", final.JWTSecret)
		fmt.Fprintln(stdout, "Store this secret; it is not printed again.")
		fmt.Fprintf(stdout, "Installed %q. Server: http://127.0.0.1:%d\n", final.InstanceName, final.ServerPort)
	} else {
		fmt.Fprintf(stdout, "Repaired %q. Server: http://127.0.0.1:%d\n", final.InstanceName, final.ServerPort)
	}
	return 0
}
