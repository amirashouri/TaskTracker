package install

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"
	"time"
)

// Distro identifies the Linux distribution for package manager selection.
type Distro string

const (
	DistroDebian  Distro = "debian"
	DistroUbuntu  Distro = "ubuntu"
	DistroFedora  Distro = "fedora"
	DistroRHEL    Distro = "rhel"
	DistroCentOS  Distro = "centos"
	DistroArch    Distro = "arch"
	DistroUnknown Distro = "unknown"
)

// DockerInstallPlan is the result of detecting the host and deciding how to
// install Docker. It is a pure function of (os-release, docker present, sudo
// available) so it can be table-tested without spawning processes.
type DockerInstallPlan struct {
	Distro         Distro
	DockerPresent  bool
	SudoAvailable  bool
	InstallCommand string
	StartCommand   string
	GroupCommand   string
	OfficialURL    string
	CanAutoInstall bool
}

// DetectDockerInstallPlan reads /etc/os-release (on Linux), checks for docker
// and sudo, and returns the plan. The platform parameter allows tests to
// override runtime.GOOS.
func DetectDockerInstallPlan(ctx context.Context, exec Executor, platform string) (DockerInstallPlan, error) {
	plan := DockerInstallPlan{
		Distro:         DistroUnknown,
		DockerPresent:  false,
		SudoAvailable:  false,
		CanAutoInstall: false,
	}

	// Check if docker is already present
	if _, err := exec.LookPath("docker"); err == nil {
		plan.DockerPresent = true
	}

	// Check if sudo is available
	if _, err := exec.LookPath("sudo"); err == nil {
		plan.SudoAvailable = true
	}

	if platform == "" {
		platform = runtime.GOOS
	}

	switch platform {
	case "linux":
		distro, err := DetectLinuxDistroFunc()
		if err != nil {
			return plan, err
		}
		plan.Distro = distro

		switch distro {
		case DistroDebian, DistroUbuntu:
			plan.InstallCommand = "sudo apt update && sudo apt install -y docker.io"
			plan.StartCommand = "sudo systemctl start docker && sudo systemctl enable docker"
			plan.GroupCommand = "sudo usermod -aG docker $USER"
			plan.OfficialURL = "https://docs.docker.com/engine/install/debian/"
			plan.CanAutoInstall = true
		case DistroFedora:
			plan.InstallCommand = "sudo dnf install -y docker"
			plan.StartCommand = "sudo systemctl start docker && sudo systemctl enable docker"
			plan.GroupCommand = "sudo usermod -aG docker $USER"
			plan.OfficialURL = "https://docs.docker.com/engine/install/fedora/"
			plan.CanAutoInstall = true
		case DistroRHEL, DistroCentOS:
			plan.InstallCommand = "sudo dnf install -y docker"
			plan.StartCommand = "sudo systemctl start docker && sudo systemctl enable docker"
			plan.GroupCommand = "sudo usermod -aG docker $USER"
			plan.OfficialURL = "https://docs.docker.com/engine/install/rhel/"
			plan.CanAutoInstall = true
		case DistroArch:
			plan.InstallCommand = "sudo pacman -S --noconfirm docker"
			plan.StartCommand = "sudo systemctl start docker && sudo systemctl enable docker"
			plan.GroupCommand = "sudo usermod -aG docker $USER"
			plan.OfficialURL = "https://docs.docker.com/engine/install/arch/"
			plan.CanAutoInstall = true
		default:
			plan.OfficialURL = "https://docs.docker.com/engine/install/"
			plan.CanAutoInstall = false
		}

	case "darwin":
		plan.Distro = DistroUnknown
		plan.InstallCommand = ""
		plan.StartCommand = "open -a Docker"
		plan.GroupCommand = ""
		plan.OfficialURL = "https://docs.docker.com/desktop/setup/install/mac-install/"
		plan.CanAutoInstall = false // Docker Desktop requires manual install

	case "windows":
		plan.Distro = DistroUnknown
		plan.InstallCommand = ""
		plan.StartCommand = "start Docker Desktop"
		plan.GroupCommand = ""
		plan.OfficialURL = "https://docs.docker.com/desktop/setup/install/windows-install/"
		plan.CanAutoInstall = false // Docker Desktop requires manual install

	default:
		plan.OfficialURL = "https://docs.docker.com/engine/install/"
		plan.CanAutoInstall = false
	}

	return plan, nil
}

var DetectLinuxDistroFunc = detectLinuxDistro

func detectLinuxDistro() (Distro, error) {
	file, err := os.Open("/etc/os-release")
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return DistroUnknown, nil
		}
		return DistroUnknown, fmt.Errorf("open /etc/os-release: %w", err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	id := ""
	idLike := ""
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "ID=") {
			id = strings.Trim(strings.TrimPrefix(line, "ID="), `"`)
		}
		if strings.HasPrefix(line, "ID_LIKE=") {
			idLike = strings.Trim(strings.TrimPrefix(line, "ID_LIKE="), `"`)
		}
	}

	// Check ID first
	switch id {
	case "ubuntu":
		return DistroUbuntu, nil
	case "debian":
		return DistroDebian, nil
	case "fedora":
		return DistroFedora, nil
	case "rhel", "centos", "rocky", "almalinux":
		return DistroRHEL, nil
	case "arch", "manjaro", "endeavouros":
		return DistroArch, nil
	}

	// Fall back to ID_LIKE
	if strings.Contains(idLike, "debian") || strings.Contains(idLike, "ubuntu") {
		return DistroDebian, nil
	}
	if strings.Contains(idLike, "fedora") || strings.Contains(idLike, "rhel") || strings.Contains(idLike, "centos") {
		return DistroRHEL, nil
	}
	if strings.Contains(idLike, "arch") {
		return DistroArch, nil
	}

	return DistroUnknown, nil
}

// RunDockerInstall executes the Docker installation plan. It requires explicit
// confirmation for each step (install, start daemon, add to docker group).
// Returns the output from the commands run.
func RunDockerInstall(ctx context.Context, executor Executor, plan DockerInstallPlan, confirmInstall, confirmStart, confirmGroup bool) (string, error) {
	var outputs []string

	if !plan.CanAutoInstall {
		return "", fmt.Errorf("automatic Docker install not supported for %s; see %s", plan.Distro, plan.OfficialURL)
	}

	if !plan.SudoAvailable {
		return "", errors.New("sudo is not available; cannot run installation commands")
	}

	// Step 1: Install Docker
	if confirmInstall && plan.InstallCommand != "" {
		cmd := executor.CommandContext(ctx, "bash", "-c", plan.InstallCommand)
		stdout, stderr, err := executor.RunWithOutput(ctx, cmd)
		outputs = append(outputs, stdout, stderr)
		if err != nil {
			return strings.Join(outputs, "\n"), fmt.Errorf("Docker install failed: %w", err)
		}
	}

	// Step 2: Start Docker daemon
	if confirmStart && plan.StartCommand != "" {
		cmd := executor.CommandContext(ctx, "bash", "-c", plan.StartCommand)
		stdout, stderr, err := executor.RunWithOutput(ctx, cmd)
		outputs = append(outputs, stdout, stderr)
		if err != nil {
			return strings.Join(outputs, "\n"), fmt.Errorf("Docker daemon start failed: %w", err)
		}
	}

	// Step 3: Wait for docker info to succeed
	if err := waitForDocker(ctx, executor); err != nil {
		return strings.Join(outputs, "\n"), fmt.Errorf("Docker daemon not ready after start: %w", err)
	}

	// Step 4: Add user to docker group (separate consent)
	if confirmGroup && plan.GroupCommand != "" {
		cmd := executor.CommandContext(ctx, "bash", "-c", plan.GroupCommand)
		stdout, stderr, err := executor.RunWithOutput(ctx, cmd)
		outputs = append(outputs, stdout, stderr)
		if err != nil {
			// Group add failure is not fatal; the user can log out/in manually
			outputs = append(outputs, "Warning: adding user to docker group failed; you may need to log out and back in.")
		}
	}

	return strings.Join(outputs, "\n"), nil
}

func waitForDocker(ctx context.Context, executor Executor) error {
	// Poll docker info up to 30 seconds
	for i := 0; i < 30; i++ {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		cmd := executor.CommandContext(ctx, "docker", "info")
		if err := executor.Run(ctx, cmd); err == nil {
			return nil
		}
		// Wait 1 second before retry
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(1 * time.Second):
		}
	}
	return errors.New("timed out waiting for docker daemon")
}
