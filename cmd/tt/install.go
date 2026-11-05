package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/amirashouri/tasktracker-server/internal/install"
)

const minAdminPasswordLength = 8

type installOptions struct {
	mode     string
	runtime  string
	provider string
	dataDir  string
	name     string
	port     int
	// serverBinary is the binary a native install supervises. Empty means
	// resolveServerBinary picks one.
	serverBinary string
	// serverBinaryURL is an https URL a native install downloads the server
	// binary from. Empty means resolveServerBinary looks on the host instead.
	// tt never phones home: the operator chooses this URL, it is not derived
	// from a version or a project endpoint.
	serverBinaryURL string
	platform        string // for testing: override runtime.GOOS

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
	// fetchBinary downloads a server binary for a native install. It is nil in
	// production and a fake in tests, so the URL branch is provable without a
	// network or a release host.
	fetchBinary func(context.Context, string, string) error
	interactive bool
}

func defaultInstallOptions() installOptions {
	return installOptions{
		mode:     string(install.ModeSQLite),
		runtime:  string(install.RuntimeDocker),
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
		prompter:     newPrompter(ctx, stdin, stdout),
		lanAddress:   install.LANAddress,
		fetchBinary:  install.FetchBinaryURL,
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
	fs.StringVar(&opts.runtime, "runtime", opts.runtime, "how the server runs: docker, or native (sqlite only, Linux with systemd)")
	fs.StringVar(&opts.provider, "provider", opts.provider, "exposure provider: lan, tailscale, or cloudflare")
	fs.StringVar(&opts.serverBinary, "server-binary", opts.serverBinary, "server binary for --runtime native (default: `server` beside tt, then /opt/tasktracker/server)")
	fs.StringVar(&opts.serverBinaryURL, "server-binary-url", opts.serverBinaryURL, "https URL to download the server binary for --runtime native; tt never phones home, so the operator chooses this URL")
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
	// --yes and a piped stdin both mean "answer nothing". Dropping the prompter
	// is what makes that fail closed: EnsureDocker refuses to install or start
	// anything when there is nobody to consent, and this is the only signal it
	// has.
	if !deps.interactive {
		deps.prompter = nil
	}

	state, err := loadState(deps.statePath)
	if err != nil {
		return fail(ctx, deps.stderr, "tt install", err)
	}
	wasInstalled := state.Version != 0

	// A repair with no flags must reproduce the instance it finds, not the
	// CLI's new-install defaults. sameInstance rejects an explicit shape change.
	if wasInstalled {
		if !opts.explicit["mode"] {
			opts.mode = string(state.Mode)
		}
		if !opts.explicit["runtime"] {
			opts.runtime = string(state.Runtime)
		}
		if !opts.explicit["server-binary"] {
			opts.serverBinary = state.BinaryPath
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
			Runtime:      install.Runtime(opts.runtime),
			Mode:         install.Mode(opts.mode),
			Provider:     install.Provider(opts.provider),
			DataDir:      opts.dataDir,
			InstanceName: opts.name,
			Version:      deps.buildVersion,
			ServerPort:   opts.port,
			BinaryPath:   opts.serverBinary,
		}
		if cmd.Runtime == install.RuntimeNative && strings.TrimSpace(cmd.BinaryPath) == "" {
			binary := state.BinaryPath
			if !wasInstalled {
				// Dry-run must not fetch: name the destination the fetch would
				// land in and let the plan print it, without touching the
				// network.
				if strings.TrimSpace(opts.serverBinaryURL) != "" {
					binary = filepath.Join(opts.dataDir, opts.name, "server")
				} else {
					resolved, err := resolveServerBinary("", "", opts.dataDir, opts.name, deps)
					if err != nil {
						return fail(ctx, deps.stderr, "tt install", err)
					}
					binary = resolved
				}
			}
			cmd.BinaryPath = binary
		}
		if !wasInstalled {
			if err := fillSecrets(&cmd); err != nil {
				return fail(ctx, deps.stderr, "tt install", err)
			}
		}
		steps, err := install.Next(state, cmd)
		if err != nil {
			return fail(ctx, deps.stderr, "tt install", err)
		}
		if cmd.Runtime == install.RuntimeNative {
			printNativePlan(deps.stdout, steps)
			if opts.serverBinaryURL != "" {
				fmt.Fprintf(deps.stdout, "# would fetch %s into %s\n", opts.serverBinaryURL, cmd.BinaryPath)
			}
			return 0
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
			return fail(ctx, deps.stderr, "tt install", err)
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

	// A native instance has no daemon to start, so Docker is not consulted.
	if state.Runtime == install.RuntimeDocker && deps.ensureDocker != nil {
		if err := deps.ensureDocker(ctx, deps.executor, deps.prompter, install.PreflightOptions{
			DataDir:  state.DataDir,
			Mode:     state.Mode,
			Port:     state.ServerPort,
			Repair:   true,
			Platform: platform,
			Out:      deps.stdout,
		}); err != nil {
			return fail(ctx, deps.stderr, "tt install", err)
		}
	}

	if err := deps.preflight(ctx, install.PreflightOptions{
		Runtime:    state.Runtime,
		DataDir:    state.DataDir,
		Mode:       state.Mode,
		Port:       state.ServerPort,
		Repair:     true,
		Platform:   platform,
		BinaryPath: state.BinaryPath,
	}); err != nil {
		return fail(ctx, deps.stderr, "tt install", err)
	}

	steps, err := install.Next(state, install.Command{
		Action:       install.ActionInstall,
		Runtime:      install.Runtime(opts.runtime),
		Mode:         install.Mode(opts.mode),
		Provider:     install.Provider(opts.provider),
		DataDir:      opts.dataDir,
		InstanceName: opts.name,
		Version:      deps.buildVersion,
		ServerPort:   opts.port,
		BinaryPath:   opts.serverBinary,
	})
	if err != nil {
		return fail(ctx, deps.stderr, "tt install", err)
	}
	if code := executeInstallSteps(ctx, steps, deps, false); code != 0 {
		return code
	}

	final := install.StateFromSteps(steps)
	fmt.Fprintf(deps.stdout, "Repaired %q. Server: http://127.0.0.1:%d\n", final.InstanceName, final.ServerPort)
	printRuntimeHint(deps.stdout, final)
	// No provider has verified a URL in this task, so this prints the
	// `tt expose` hint rather than a link to an address a phone cannot reach.
	// T9.8+ pass the URL they verified here.
	printPairing(deps.stdout, final.Provider, "")
	return 0
}

func freshInstall(ctx context.Context, opts installOptions, deps installDeps) int {
	if err := validateInstallShape(opts, deps.buildVersion); err != nil {
		return fail(ctx, deps.stderr, "tt install", err)
	}

	// Fail before any prompt or step if a non-interactive invocation cannot
	// supply the first admin. The wizard must not quietly install an instance
	// nobody can sign in to, and this check costs nothing to run first.
	if !deps.interactive {
		if err := validateAdminFlags(opts); err != nil {
			return fail(ctx, deps.stderr, "tt install", err)
		}
	}

	platform := opts.platform
	if platform == "" {
		platform = runtime.GOOS
	}

	// The runtime is a choice, not a prerequisite: a SQLite install can run
	// without Docker at all. Settling it before the Docker bootstrap is what
	// keeps that bootstrap from running for a customer who asked for neither.
	if deps.interactive && !opts.explicit["runtime"] {
		mode, err := askMode(deps.prompter, deps.stdout, opts.mode)
		if err != nil {
			return fail(ctx, deps.stderr, "tt install", err)
		}
		opts.mode = string(mode)

		if install.Mode(opts.mode) == install.ModeSQLite {
			native, err := deps.prompter.Confirm("Run the server without Docker (a systemd service on this host)?", false)
			if err != nil {
				return fail(ctx, deps.stderr, "tt install", err)
			}
			if native {
				opts.runtime = string(install.RuntimeNative)
			}
		}
		// The database answer just changed, so the shape checks run again with
		// the mode the customer actually chose.
		if err := validateInstallShape(opts, deps.buildVersion); err != nil {
			return fail(ctx, deps.stderr, "tt install", err)
		}
	}

	// --server-binary-url only makes sense for a native install: a Docker
	// install supervises an image, and downloading a binary into a compose
	// project is not a thing. Refuse it before the Docker bootstrap so the
	// customer meets the refusal, not a compose error.
	if install.Runtime(opts.runtime) != install.RuntimeNative && strings.TrimSpace(opts.serverBinaryURL) != "" {
		return fail(ctx, deps.stderr, "tt install", fmt.Errorf("--server-binary-url is only for --runtime native; a Docker install pulls the image"))
	}

	// Docker is only needed by a Docker install, and it is ensured before the
	// remaining questions so nobody answers a path the install cannot use.
	if install.Runtime(opts.runtime) == install.RuntimeDocker && deps.ensureDocker != nil {
		if err := deps.ensureDocker(ctx, deps.executor, deps.prompter, install.PreflightOptions{
			Platform: platform,
			Out:      deps.stdout,
		}); err != nil {
			return fail(ctx, deps.stderr, "tt install", err)
		}
	}

	if deps.interactive {
		dataDir, err := askDataDir(deps.prompter, opts.dataDir)
		if err != nil {
			return fail(ctx, deps.stderr, "tt install", err)
		}
		opts.dataDir = dataDir
	}

	dataDir, err := normalizeDataDir(opts.dataDir)
	if err != nil {
		return fail(ctx, deps.stderr, "tt install", err)
	}
	opts.dataDir = dataDir

	// A native install supervises a binary that is already on the host. When
	// the operator passes --server-binary-url, tt fetches it into the data
	// directory — the same directory the service account will own — and only
	// then, so the path is absolute and the directory exists.
	if install.Runtime(opts.runtime) == install.RuntimeNative {
		binary, err := resolveServerBinary(opts.serverBinary, opts.serverBinaryURL, opts.dataDir, opts.name, deps)
		if err != nil {
			return fail(ctx, deps.stderr, "tt install", err)
		}
		opts.serverBinary = binary
	}

	if err := deps.preflight(ctx, install.PreflightOptions{
		Runtime:    install.Runtime(opts.runtime),
		DataDir:    opts.dataDir,
		Mode:       install.Mode(opts.mode),
		Port:       opts.port,
		Platform:   platform,
		BinaryPath: opts.serverBinary,
	}); err != nil {
		return fail(ctx, deps.stderr, "tt install", err)
	}

	var admin *install.AdminBootstrap
	if deps.interactive {
		admin, err = askAdmin(deps.prompter, deps.stdout)
		if err != nil {
			return fail(ctx, deps.stderr, "tt install", err)
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
		Runtime:          install.Runtime(opts.runtime),
		Mode:             install.Mode(opts.mode),
		Provider:         install.Provider(opts.provider),
		DataDir:          opts.dataDir,
		InstanceName:     opts.name,
		Version:          deps.buildVersion,
		ServerPort:       opts.port,
		BinaryPath:       opts.serverBinary,
		AdminEmail:       admin.Email,
		AdminPassword:    admin.Password,
		AdminDisplayName: admin.DisplayName,
	}
	if err := fillSecrets(&cmd); err != nil {
		return fail(ctx, deps.stderr, "tt install", err)
	}

	steps, err := install.Next(install.State{Path: deps.statePath}, cmd)
	if err != nil {
		return fail(ctx, deps.stderr, "tt install", err)
	}
	if code := executeInstallSteps(ctx, steps, deps, true); code != 0 {
		return code
	}

	final := install.StateFromSteps(steps)
	fmt.Fprintf(deps.stdout, "Installed %q. Server: http://127.0.0.1:%d\n", final.InstanceName, final.ServerPort)
	fmt.Fprintf(deps.stdout, "Admin: %s\n", admin.Email)
	printRuntimeHint(deps.stdout, final)
	printLAN(deps.stdout, final, deps.lanAddress)
	fmt.Fprintf(deps.stdout, "Next: open http://127.0.0.1:%d on this host and sign in as %s.\n", final.ServerPort, admin.Email)
	// No provider has verified a URL in this task, so this prints the
	// `tt expose` hint rather than a link to a loopback address a phone cannot
	// reach. T9.8+ pass the URL they verified here; the pairing step is last
	// because it is the one that makes the server reachable from the phone.
	printPairing(deps.stdout, final.Provider, "")
	return 0
}

// resolveServerBinary prefers an explicit path, then a URL the operator
// chose, then `server` beside the tt binary (the release bundle ships both),
// then the path docs/SELF-HOSTING.md installs to. tt never downloads a binary
// on its own: a URL is an explicit argument, and nothing here derives one.
//
// When a URL is given, the binary is fetched into the data directory next to
// the instance, so a repair reuses the file that is already there and only
// re-fetches when the operator passes the URL again.
func resolveServerBinary(explicit, url, dataDir, name string, deps installDeps) (string, error) {
	if strings.TrimSpace(explicit) != "" {
		absolute, err := filepath.Abs(explicit)
		if err != nil {
			return "", fmt.Errorf("resolve --server-binary %s: %w", explicit, err)
		}
		return absolute, nil
	}
	if strings.TrimSpace(url) != "" {
		if strings.TrimSpace(dataDir) == "" {
			return "", errors.New("--server-binary-url needs --data-dir or --name to know where to put the binary")
		}
		dest := filepath.Join(dataDir, name, "server")
		if install.BinaryExists(dest) {
			return dest, nil
		}
		if deps.fetchBinary == nil {
			return "", errors.New("--server-binary-url needs a fetch function; the production tt installs one")
		}
		fmt.Fprintf(deps.stdout, "Fetching the server binary from %s into %s\n", url, dest)
		if err := deps.fetchBinary(context.Background(), url, dest); err != nil {
			if install.IsHTTPRefused(err) {
				return "", err
			}
			return "", fmt.Errorf("fetch %s: %w", url, err)
		}
		return dest, nil
	}

	if self, err := os.Executable(); err == nil {
		candidate := filepath.Join(filepath.Dir(self), "server")
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate, nil
		}
	}
	if info, err := os.Stat(install.NativeBinaryPath); err == nil && !info.IsDir() {
		return install.NativeBinaryPath, nil
	}
	return "", errors.New("no server binary found: pass --server-binary, or put `server` beside tt. tt never downloads one")
}

// printNativePlan is the dry run's output for a native install: the two files
// it would write, with the paths they would land on.
func printNativePlan(w io.Writer, steps []install.Step) {
	for _, step := range steps {
		switch step.Kind {
		case install.StepWriteEnv:
			fmt.Fprintf(w, "# %s\n%s", step.State.EnvPath, step.Env)
		case install.StepWriteUnit:
			fmt.Fprintf(w, "# %s\n%s", step.State.UnitPath, step.Unit)
		}
	}
}

// printRuntimeHint names the thing that supervises the instance, which is what
// an operator reaches for first when the server stops answering.
func printRuntimeHint(w io.Writer, state install.State) {
	if state.Runtime == install.RuntimeNative {
		fmt.Fprintf(w, "Service: %s (status: systemctl status %s, logs: journalctl -u %s)\n",
			state.ServiceName(), state.ServiceName(), state.ServiceName())
		return
	}
	fmt.Fprintf(w, "Compose: %s\n", state.ComposePath)
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
			return fail(ctx, deps.stderr, "tt install", err)
		}
		if printSecret && step.Kind == install.StepWriteState {
			fmt.Fprintf(deps.stdout, "JWT_SECRET=%s\n", step.State.JWTSecret)
			fmt.Fprintln(deps.stdout, "Store this secret; it is not printed again.")
		}
	}
	return 0
}
