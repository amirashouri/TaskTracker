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

// PreflightOptions names what the checks depend on. Platform is normally
// runtime.GOOS; tests set it to exercise the start hint.
type PreflightOptions struct {
	DataDir  string
	Mode     Mode
	Port     int
	Repair   bool
	Platform string
}

func (o PreflightOptions) platform() string {
	if o.Platform == "" {
		return runtime.GOOS
	}
	return o.Platform
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
	switch opts.Mode {
	case ModeSQLite, ModePostgres:
	default:
		return fmt.Errorf("database mode %q is not sqlite or postgres; pass --mode", opts.Mode)
	}
	if opts.Port < 1 || opts.Port > 65535 {
		return fmt.Errorf("port %d is not between 1 and 65535; pass --port", opts.Port)
	}

	if _, err := env.LookPath("docker"); err != nil {
		return fmt.Errorf("Docker is not installed. %s and re-run `tt install`", dockerInstallHint(opts.platform()))
	}

	dockerRoot, err := env.DockerRoot(ctx)
	if err != nil {
		return fmt.Errorf("Docker is installed but its daemon is not reachable (%v). %s, then re-run `tt install`", err, dockerStartHint(opts.platform()))
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
	if opts.platform() == "linux" && dockerRoot != "" {
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
