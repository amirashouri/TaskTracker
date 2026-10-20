package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"runtime"
	"strings"

	"github.com/amirashouri/tasktracker-server/internal/install"
)

const minAdminPasswordLength = 8

type installOptions struct {
	mode     string
	provider string
	dataDir  string
	name     string
	port     int
	platform string // for testing: override runtime.GOOS

	dryRun bool
	yes    bool

	adminEmail       string
	adminPassword    string
	adminDisplayName string

	explicit map[string]bool
}

type installDeps struct {
	stdin          io.Reader
	stdout, stderr io.Writer
	statePath      string
	buildVersion   string
	runner         install.Runner
	preflight      func(context.Context, install.PreflightOptions) error
	ensureDocker   func(context.Context, install.Executor, install.Prompter, install.PreflightOptions) error
	executor       install.Executor
	prompter       prompter
	lanAddress     func() string
	interactive    bool
}

func defaultInstallOptions() installOptions {
	return installOptions{
		mode:     string(install.ModeSQLite),
		provider: string(install.ProviderLAN),
		dataDir:  install.DefaultDataDir(),
		name:     "tasktracker",
		port:     8080,
	}
}

func runInstall(
	ctx context.Context,
	args []string,
	stdin io.Reader,
	stdout, stderr io.Writer,
	statePath, buildVersion string,
	runner install.Runner,
) int {
	return runInstallWithDeps(ctx, args, installDeps{
		stdin:        stdin,
		stdout:       stdout,
		stderr:       stderr,
		statePath:    statePath,
		buildVersion: buildVersion,
		runner:       runner,
		preflight:    install.Preflight,
		ensureDocker: install.EnsureDocker,
		executor:     install.RealExecutor{},
		prompter:     newPrompter(stdin, stdout),
		lanAddress:   install.LANAddress,
	})
}

// runInstallWithDeps parses the install flags against injectable dependencies.
// runInstall supplies the production set; tests supply fakes so flag-level
// behavior is provable without a Docker daemon.
func runInstallWithDeps(ctx context.Context, args []string, deps installDeps) int {
	opts := defaultInstallOptions()

	fs := flag.NewFlagSet("install", flag.ContinueOnError)
	fs.SetOutput(deps.stderr)
	fs.StringVar(&opts.mode, "mode", opts.mode, "database mode: sqlite or postgres")
	fs.StringVar(&opts.provider, "provider", opts.provider, "exposure provider: lan, tailscale, or cloudflare")
	fs.StringVar(&opts.dataDir, "data-dir", opts.dataDir, "directory for the rendered compose file")
	fs.StringVar(&opts.name, "name", opts.name, "instance name")
	fs.IntVar(&opts.port, "port", opts.port, "host port for the HTTP server")
	fs.BoolVar(&opts.dryRun, "dry-run", false, "print the rendered compose and execute nothing")
	fs.BoolVar(&opts.yes, "yes", false, "use non-interactive defaults; requires --admin-email and --admin-password")
	fs.StringVar(&opts.adminEmail, "admin-email", "", "first admin email (non-interactive installs)")
	fs.StringVar(&opts.adminPassword, "admin-password", "", "first admin password (non-interactive installs; visible in process arguments)")
	fs.StringVar(&opts.adminDisplayName, "admin-display-name", "", "first admin display name")
	if code, done := parseFlags(fs, args, deps.stderr); done {
		return code
	}

	opts.explicit = map[string]bool{}
	fs.Visit(func(f *flag.Flag) { opts.explicit[f.Name] = true })
	deps.interactive = !opts.yes && isTerminal(deps.stdin)

	return installWith(ctx, opts, deps)
}

// installWith is the testable wizard. Flag parsing and the production
// dependencies live in runInstall; every decision here is injectable.
func installWith(ctx context.Context, opts installOptions, deps installDeps) int {
	if deps.preflight == nil {
		deps.preflight = install.Preflight
	}
	if deps.lanAddress == nil {
		deps.lanAddress = install.LANAddress
	}

	state, err := loadState(deps.statePath)
	if err != nil {
		fmt.Fprintf(deps.stderr, "tt install: %v\n", err)
		return 1
	}
	wasInstalled := state.Version != 0

	// A repair with no flags must reproduce the instance it finds, not the
	// CLI's new-install defaults. sameInstance rejects an explicit shape change.
	if wasInstalled {
		if !opts.explicit["mode"] {
			opts.mode = string(state.Mode)
		}
		if !opts.explicit["provider"] {
			opts.provider = string(state.Provider)
		}
		if !opts.explicit["data-dir"] {
			opts.dataDir = state.DataDir
		}
		if !opts.explicit["name"] {
			opts.name = state.InstanceName
		}
		if !opts.explicit["port"] {
			opts.port = state.ServerPort
		}
	}

	// Dry-run is deliberately before preflight and prompts. It must work on a
	// machine with no Docker daemon and leave no state behind.
	if opts.dryRun {
		cmd := install.Command{
			Action:       install.ActionInstall,
			Mode:         install.Mode(opts.mode),
			Provider:     install.Provider(opts.provider),
			DataDir:      opts.dataDir,
			InstanceName: opts.name,
			Version:      deps.buildVersion,
			ServerPort:   opts.port,
		}
		if !wasInstalled {
			if err := fillSecrets(&cmd); err != nil {
				fmt.Fprintf(deps.stderr, "tt install: %v\n", err)
				return 1
			}
		}
		steps, err := install.Next(state, cmd)
		if err != nil {
			fmt.Fprintf(deps.stderr, "tt install: %v\n", err)
			return 1
		}
		compose := install.ComposeFromSteps(steps)
		if compose == "" {
			fmt.Fprintln(deps.stderr, "tt install: the plan produced no compose file")
			return 1
		}
		fmt.Fprint(deps.stdout, compose)
		return 0
	}

	if wasInstalled {
		return repairInstall(ctx, opts, state, deps)
	}
	return freshInstall(ctx, opts, deps)
}

func repairInstall(ctx context.Context, opts installOptions, state install.State, deps installDeps) int {
	if deps.interactive {
		ok, err := deps.prompter.Confirm(fmt.Sprintf("Repair the existing %q install?", state.InstanceName), true)
		if err != nil {
			fmt.Fprintf(deps.stderr, "tt install: %v\n", err)
			return 1
		}
		if !ok {
			fmt.Fprintln(deps.stdout, "Nothing changed.")
			return 0
		}
	}

	platform := opts.platform
	if platform == "" {
		platform = runtime.GOOS
	}

	// Ensure Docker is installed and running before proceeding with preflight
	if deps.ensureDocker != nil {
		if err := deps.ensureDocker(ctx, deps.executor, deps.prompter, install.PreflightOptions{
			DataDir:  state.DataDir,
			Mode:     state.Mode,
			Port:     state.ServerPort,
			Repair:   true,
			Platform: platform,
		}); err != nil {
			fmt.Fprintf(deps.stderr, "tt install: %v\n", err)
			return 1
		}
	}

	if err := deps.preflight(ctx, install.PreflightOptions{
		DataDir:  state.DataDir,
		Mode:     state.Mode,
		Port:     state.ServerPort,
		Repair:   true,
		Platform: platform,
	}); err != nil {
		fmt.Fprintf(deps.stderr, "tt install: %v\n", err)
		return 1
	}

	steps, err := install.Next(state, install.Command{
		Action:       install.ActionInstall,
		Mode:         install.Mode(opts.mode),
		Provider:     install.Provider(opts.provider),
		DataDir:      opts.dataDir,
		InstanceName: opts.name,
		Version:      deps.buildVersion,
		ServerPort:   opts.port,
	})
	if err != nil {
		fmt.Fprintf(deps.stderr, "tt install: %v\n", err)
		return 1
	}
	if code := executeInstallSteps(ctx, steps, deps, false); code != 0 {
		return code
	}

	final := install.StateFromSteps(steps)
	fmt.Fprintf(deps.stdout, "Repaired %q. Server: http://127.0.0.1:%d\n", final.InstanceName, final.ServerPort)
	// No provider has verified a URL in this task, so this prints the
	// `tt expose` hint rather than a link to an address a phone cannot reach.
	// T9.8+ pass the URL they verified here.
	printPairing(deps.stdout, final.Provider, "")
	return 0
}

func freshInstall(ctx context.Context, opts installOptions, deps installDeps) int {
	if err := validateInstallShape(opts, deps.buildVersion); err != nil {
		fmt.Fprintf(deps.stderr, "tt install: %v\n", err)
		return 1
	}

	if deps.interactive {
		mode, err := askMode(deps.prompter, deps.stdout, opts.mode)
		if err != nil {
			fmt.Fprintf(deps.stderr, "tt install: %v\n", err)
			return 1
		}
		opts.mode = string(mode)

		dataDir, err := askDataDir(deps.prompter, opts.dataDir)
		if err != nil {
			fmt.Fprintf(deps.stderr, "tt install: %v\n", err)
			return 1
		}
		opts.dataDir = dataDir
	}

	dataDir, err := normalizeDataDir(opts.dataDir)
	if err != nil {
		fmt.Fprintf(deps.stderr, "tt install: %v\n", err)
		return 1
	}
	opts.dataDir = dataDir

	// Fail before preflight or any step if a non-interactive invocation cannot
	// supply the first admin. The wizard must not quietly install an instance
	// nobody can sign in to.
	if !deps.interactive {
		if err := validateAdminFlags(opts); err != nil {
			fmt.Fprintf(deps.stderr, "tt install: %v\n", err)
			return 1
		}
	}

	platform := opts.platform
	if platform == "" {
		platform = runtime.GOOS
	}

	// Ensure Docker is installed and running before proceeding with preflight
	if deps.ensureDocker != nil {
		if err := deps.ensureDocker(ctx, deps.executor, deps.prompter, install.PreflightOptions{
			DataDir:  opts.dataDir,
			Mode:     install.Mode(opts.mode),
			Port:     opts.port,
			Platform: platform,
		}); err != nil {
			fmt.Fprintf(deps.stderr, "tt install: %v\n", err)
			return 1
		}
	}

	if err := deps.preflight(ctx, install.PreflightOptions{
		DataDir:  opts.dataDir,
		Mode:     install.Mode(opts.mode),
		Port:     opts.port,
		Platform: platform,
	}); err != nil {
		fmt.Fprintf(deps.stderr, "tt install: %v\n", err)
		return 1
	}

	var admin *install.AdminBootstrap
	if deps.interactive {
		admin, err = askAdmin(deps.prompter, deps.stdout)
		if err != nil {
			fmt.Fprintf(deps.stderr, "tt install: %v\n", err)
			return 1
		}
	} else {
		admin = &install.AdminBootstrap{
			Email:       strings.TrimSpace(opts.adminEmail),
			Password:    opts.adminPassword,
			DisplayName: strings.TrimSpace(opts.adminDisplayName),
		}
	}

	cmd := install.Command{
		Action:           install.ActionInstall,
		Mode:             install.Mode(opts.mode),
		Provider:         install.Provider(opts.provider),
		DataDir:          opts.dataDir,
		InstanceName:     opts.name,
		Version:          deps.buildVersion,
		ServerPort:       opts.port,
		AdminEmail:       admin.Email,
		AdminPassword:    admin.Password,
		AdminDisplayName: admin.DisplayName,
	}
	if err := fillSecrets(&cmd); err != nil {
		fmt.Fprintf(deps.stderr, "tt install: %v\n", err)
		return 1
	}

	steps, err := install.Next(install.State{Path: deps.statePath}, cmd)
	if err != nil {
		fmt.Fprintf(deps.stderr, "tt install: %v\n", err)
		return 1
	}
	if code := executeInstallSteps(ctx, steps, deps, true); code != 0 {
		return code
	}

	final := install.StateFromSteps(steps)
	fmt.Fprintf(deps.stdout, "Installed %q. Server: http://127.0.0.1:%d\n", final.InstanceName, final.ServerPort)
	fmt.Fprintf(deps.stdout, "Admin: %s\n", admin.Email)
	printLAN(deps.stdout, final, deps.lanAddress)
	fmt.Fprintf(deps.stdout, "Next: open http://127.0.0.1:%d on this host and sign in as %s.\n", final.ServerPort, admin.Email)
	// No provider has verified a URL in this task, so this prints the
	// `tt expose` hint rather than a link to a loopback address a phone cannot
	// reach. T9.8+ pass the URL they verified here; the pairing step is last
	// because it is the one that makes the server reachable from the phone.
	printPairing(deps.stdout, final.Provider, "")
	return 0
}

func fillSecrets(cmd *install.Command) error {
	secret, err := install.NewSecret()
	if err != nil {
		return err
	}
	cmd.Secret = secret

	if cmd.Mode == install.ModePostgres {
		password, err := install.NewSecret()
		if err != nil {
			return err
		}
		cmd.DBPassword = password
	}
	return nil
}

// executeInstallSteps prints the secret as soon as the state write succeeds.
// A later /readyz or admin failure must not leave the customer without the one
// value the CLI will never print again.
func executeInstallSteps(ctx context.Context, steps []install.Step, deps installDeps, printSecret bool) int {
	for _, step := range steps {
		if err := deps.runner.Run(ctx, step); err != nil {
			fmt.Fprintf(deps.stderr, "tt install: %v\n", err)
			return 1
		}
		if printSecret && step.Kind == install.StepWriteState {
			fmt.Fprintf(deps.stdout, "JWT_SECRET=%s\n", step.State.JWTSecret)
			fmt.Fprintln(deps.stdout, "Store this secret; it is not printed again.")
		}
	}
	return 0
}
