package install

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

// preflightEnv is the small surface preflight needs from the host. It exists so
// the checks can be proved without a Docker daemon, a real port, or a real
// filesystem failure.
type preflightEnv interface {
	LookPath(name string) (string, error)
	DockerRoot(ctx context.Context) (string, error)
	Listen(network, address string) (io.Closer, error)
	EnsureWritable(path string) error
	AvailableBytes(path string) (uint64, error)
	Stat(path string) (os.FileInfo, error)
	Euid() int
}

type osPreflightEnv struct{}

func (osPreflightEnv) LookPath(name string) (string, error) {
	return exec.LookPath(name)
}

func (osPreflightEnv) DockerRoot(ctx context.Context) (string, error) {
	cmd := exec.CommandContext(ctx, "docker", "info", "--format", "{{.DockerRootDir}}")
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail != "" {
			return "", fmt.Errorf("%w: %s", err, detail)
		}
		return "", err
	}
	return strings.TrimSpace(stdout.String()), nil
}

func (osPreflightEnv) Listen(network, address string) (io.Closer, error) {
	return net.Listen(network, address)
}

func (osPreflightEnv) EnsureWritable(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	file, err := os.CreateTemp(path, ".tt-write-*")
	if err != nil {
		return err
	}
	name := file.Name()
	if err := file.Close(); err != nil {
		_ = os.Remove(name)
		return err
	}
	return os.Remove(name)
}

func (osPreflightEnv) AvailableBytes(path string) (uint64, error) {
	return availableBytes(path)
}

func (osPreflightEnv) Stat(path string) (os.FileInfo, error) {
	return os.Stat(path)
}

func (osPreflightEnv) Euid() int {
	return os.Geteuid()
}

// PreflightOptions names what the checks depend on. Platform is normally
// runtime.GOOS; tests set it to exercise the start hint.
type PreflightOptions struct {
	Runtime    Runtime
	DataDir    string
	Mode       Mode
	Port       int
	Repair     bool
	Platform   string
	BinaryPath string

	// Out is where EnsureDocker reports the plan it would run and what it saw.
	// Nil means os.Stderr. The wizard passes its stdout so the bootstrap reads
	// in order with its own prompts instead of jumping between streams.
	Out io.Writer
}

func (o PreflightOptions) platform() string {
	if o.Platform == "" {
		return runtime.GOOS
	}
	return o.Platform
}

func (o PreflightOptions) output() io.Writer {
	if o.Out != nil {
		return o.Out
	}
	return os.Stderr
}

// Preflight refuses an install that cannot work. Every error names the fix:
// install Docker, start it, choose another directory or port, or free space.
func Preflight(ctx context.Context, opts PreflightOptions) error {
	return preflight(ctx, osPreflightEnv{}, opts)
}

func preflight(ctx context.Context, env preflightEnv, opts PreflightOptions) error {
	if strings.TrimSpace(opts.DataDir) == "" {
		return errors.New("a data directory is required; pass --data-dir")
	}
	runtime := opts.Runtime
	if runtime == "" {
		runtime = RuntimeDocker
	}
	switch runtime {
	case RuntimeDocker, RuntimeNative:
	default:
		return fmt.Errorf("runtime %q is not docker or native; pass --runtime", opts.Runtime)
	}
	switch opts.Mode {
	case ModeSQLite, ModePostgres:
	default:
		return fmt.Errorf("database mode %q is not sqlite or postgres; pass --mode", opts.Mode)
	}
	if opts.Port < 1 || opts.Port > 65535 {
		return fmt.Errorf("port %d is not between 1 and 65535; pass --port", opts.Port)
	}

	var dockerRoot string
	if runtime == RuntimeNative {
		if err := nativeHostChecks(env, opts); err != nil {
			return err
		}
	} else {
		if _, err := env.LookPath("docker"); err != nil {
			return fmt.Errorf("Docker is not installed. %s and re-run `tt install`", dockerInstallHint(opts.platform()))
		}

		root, err := env.DockerRoot(ctx)
		if err != nil {
			return fmt.Errorf("Docker is installed but its daemon is not reachable (%v). %s, then re-run `tt install`", err, dockerStartHint(opts.platform()))
		}
		dockerRoot = root
	}

	if err := env.EnsureWritable(opts.DataDir); err != nil {
		return fmt.Errorf("data directory %s is not writable: %v. Choose a writable directory with --data-dir or fix its permissions", opts.DataDir, err)
	}

	if !opts.Repair {
		listener, err := env.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", opts.Port))
		if err != nil {
			return fmt.Errorf("port %d is already in use: %v. Choose another with --port or stop the process using it", opts.Port, err)
		}
		_ = listener.Close()
	}

	// Docker Desktop stores its VM disk on the host, but reports a path inside
	// the VM. On Linux the Docker root is the real storage location; elsewhere
	// the data directory's filesystem is the useful host proxy.
	target := opts.DataDir
	if runtime == RuntimeDocker && opts.platform() == "linux" && dockerRoot != "" {
		target = dockerRoot
	}
	available, err := env.AvailableBytes(target)
	if err != nil && target != opts.DataDir {
		target = opts.DataDir
		available, err = env.AvailableBytes(target)
	}
	if err != nil {
		return fmt.Errorf("check free space in %s: %v. Choose a readable --data-dir or check its permissions", target, err)
	}
	if available == 0 {
		return nil
	}

	required := minFreeBytes(opts.Mode)
	if available < required {
		return fmt.Errorf("not enough free space in %s: need at least %s, have %s. Free space or choose another --data-dir", target, humanBytes(required), humanBytes(available))
	}
	return nil
}

// nativeHostChecks refuses a native install that cannot run. It needs root (the
// unit is written to /etc/systemd/system), systemd to run it, SQLite as the
// database, and a server binary that exists and is executable.
func nativeHostChecks(env preflightEnv, opts PreflightOptions) error {
	if opts.Mode != ModeSQLite {
		return errors.New("a native install runs SQLite only; use --mode postgres with --runtime docker")
	}
	if _, err := env.LookPath("systemctl"); err != nil {
		return errors.New("a native install needs systemd, and systemctl is not on PATH. Use --runtime docker, or follow docs/SELF-HOSTING.md")
	}
	if env.Euid() != 0 {
		return errors.New("a native install writes /etc/systemd/system and needs root; re-run with sudo, or use --runtime docker")
	}
	if strings.TrimSpace(opts.BinaryPath) == "" {
		return errors.New("a native install needs a server binary; pass --server-binary")
	}
	info, err := env.Stat(opts.BinaryPath)
	if err != nil {
		return fmt.Errorf("server binary %s is not readable: %v. Pass --server-binary, or install with --runtime docker", opts.BinaryPath, err)
	}
	if info.IsDir() {
		return fmt.Errorf("server binary %s is a directory; pass the binary itself with --server-binary", opts.BinaryPath)
	}
	if info.Mode()&0o111 == 0 {
		return fmt.Errorf("server binary %s is not executable; run chmod +x %s and re-run `tt install`", opts.BinaryPath, opts.BinaryPath)
	}
	return nil
}

func dockerInstallHint(platform string) string {
	switch platform {
	case "darwin":
		return "Install Docker Desktop from https://docs.docker.com/desktop/setup/install/mac-install/"
	case "windows":
		return "Install Docker Desktop from https://docs.docker.com/desktop/setup/install/windows-install/"
	default:
		return "Install Docker Engine from https://docs.docker.com/engine/install/"
	}
}

func dockerStartHint(platform string) string {
	switch platform {
	case "linux":
		return "Start it with `sudo systemctl start docker` (or start Docker Desktop)"
	case "darwin", "windows":
		return "Start Docker Desktop"
	default:
		return "Start Docker"
	}
}

func minFreeBytes(mode Mode) uint64 {
	if mode == ModePostgres {
		return 2 << 30
	}
	return 512 << 20
}

func humanBytes(bytes uint64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	div, exp := uint64(unit), 0
	for n := bytes / unit; n >= unit && exp < 5; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(bytes)/float64(div), "KMGTPE"[exp])
}

// Prompter is the interface for interactive prompts. The CLI provides a real
// implementation; tests provide a fake.
type Prompter interface {
	Confirm(prompt string, defaultYes bool) (bool, error)
}

// EnsureDocker checks if Docker is installed and running. If not, it offers
// to install it via the distribution's package manager (Linux) or prints
// the manual install URL (macOS/Windows). It requires explicit confirmation
// for each step and fails closed in non-interactive mode.
// The prompter is called for each confirmation; if nil, the function behaves
// as non-interactive (fails closed).
func EnsureDocker(ctx context.Context, exec Executor, prompter Prompter, opts PreflightOptions) error {
	plan, err := DetectDockerInstallPlan(ctx, exec, opts.Platform)
	if err != nil {
		return fmt.Errorf("detect Docker install plan: %w", err)
	}

	// If Docker is already present and daemon is reachable, we're good
	if plan.DockerPresent {
		// Quick check that daemon is reachable
		cmd := exec.CommandContext(ctx, "docker", "info")
		if err := exec.Run(ctx, cmd); err == nil {
			return nil
		}
		// Docker binary exists but daemon not running - try to start it
		if plan.StartCommand != "" {
			return startDockerDaemon(ctx, exec, prompter, plan, opts)
		}
		return fmt.Errorf("Docker is installed but daemon is not reachable. %s", dockerStartHint(opts.platform()))
	}

	// Docker not installed
	if !plan.CanAutoInstall {
		return fmt.Errorf("Docker is not installed. %s", plan.OfficialURL)
	}

	// Nobody to consent means nothing is run: --yes and a piped stdin both
	// arrive here with a nil prompter.
	if prompter == nil {
		return fmt.Errorf("Docker is not installed and this install is non-interactive. Automatic Docker install is not allowed. Install Docker manually: %s", plan.OfficialURL)
	}

	w := opts.output()

	// Interactive mode: offer to install
	fmt.Fprintf(w, "Docker is not installed. This install can set it up for you.\n")
	fmt.Fprintf(w, "Distribution detected: %s\n", plan.Distro)
	fmt.Fprintf(w, "The following command will be run:\n  %s\n", plan.InstallCommand)
	fmt.Fprintf(w, "This requires sudo. You will be prompted for your password.\n")

	confirmInstall, err := prompter.Confirm("Install Docker now?", false)
	if err != nil {
		return fmt.Errorf("prompt failed: %w", err)
	}
	if !confirmInstall {
		return fmt.Errorf("Docker install declined. Please install Docker manually: %s", plan.OfficialURL)
	}

	fmt.Fprintf(w, "The Docker daemon will be started and enabled:\n  %s\n", plan.StartCommand)
	confirmStart, err := prompter.Confirm("Start Docker daemon now?", false)
	if err != nil {
		return fmt.Errorf("prompt failed: %w", err)
	}
	if !confirmStart {
		return fmt.Errorf("Docker daemon start declined. Please start Docker manually.")
	}

	fmt.Fprintf(w, "Adding your user to the 'docker' group allows running Docker without sudo.\n")
	fmt.Fprintf(w, "This is root-equivalent on the host and requires a separate consent.\n")
	fmt.Fprintf(w, "Command: %s\n", plan.GroupCommand)
	confirmGroup, err := prompter.Confirm("Add your user to the docker group?", false)
	if err != nil {
		return fmt.Errorf("prompt failed: %w", err)
	}

	// Run the installation
	output, err := RunDockerInstall(ctx, exec, plan, confirmInstall, confirmStart, confirmGroup)
	if err != nil {
		if output != "" {
			fmt.Fprintf(w, "%s\n", output)
		}
		return fmt.Errorf("Docker bootstrap failed: %w", err)
	}

	if output != "" {
		fmt.Fprintf(w, "%s\n", output)
	}

	fmt.Fprintf(w, "Docker is now installed and running.\n")
	if confirmGroup {
		fmt.Fprintf(w, "You may need to log out and back in for the docker group change to take effect.\n")
	}
	return nil
}

func startDockerDaemon(ctx context.Context, executor Executor, prompter Prompter, plan DockerInstallPlan, opts PreflightOptions) error {
	w := opts.output()

	fmt.Fprintf(w, "Docker is installed but its daemon is not running.\n")
	fmt.Fprintf(w, "The following command will start and enable it:\n  %s\n", plan.StartCommand)

	if prompter == nil {
		return fmt.Errorf("Docker daemon not running and this install is non-interactive. Start Docker manually: %s", dockerStartHint(opts.platform()))
	}

	confirmStart, err := prompter.Confirm("Start Docker daemon now?", false)
	if err != nil {
		return fmt.Errorf("prompt failed: %w", err)
	}
	if !confirmStart {
		return fmt.Errorf("Docker daemon start declined. Please start Docker manually.")
	}

	cmd := executor.CommandContext(ctx, "bash", "-c", plan.StartCommand)
	stdout, stderr, err := executor.RunWithOutput(ctx, cmd)
	if err != nil {
		if stdout != "" {
			fmt.Fprintf(w, "%s\n", stdout)
		}
		if stderr != "" {
			fmt.Fprintf(w, "%s\n", stderr)
		}
		return fmt.Errorf("Docker daemon start failed: %w", err)
	}

	if err := waitForDocker(ctx, executor); err != nil {
		return fmt.Errorf("Docker daemon not ready after start: %w", err)
	}

	fmt.Fprintf(w, "Docker daemon is now running.\n")
	return nil
}
