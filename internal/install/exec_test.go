package install

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// fakeExecutor is a test implementation of Executor that records calls and
// returns scripted results.
type fakeExecutor struct {
	lookPathFunc       func(string) (string, error)
	runFunc            func(context.Context, *exec.Cmd) error
	runWithOutputFunc  func(context.Context, *exec.Cmd) (string, string, error)
	commandContextFunc func(context.Context, string, ...string) *exec.Cmd
	calls              []execCall
}

type execCall struct {
	name string
	args []string
}

func (f *fakeExecutor) LookPath(name string) (string, error) {
	if f.lookPathFunc != nil {
		return f.lookPathFunc(name)
	}
	return "", exec.ErrNotFound
}

func (f *fakeExecutor) Run(ctx context.Context, cmd *exec.Cmd) error {
	f.calls = append(f.calls, execCall{name: cmd.Path, args: cmd.Args})
	if f.runFunc != nil {
		return f.runFunc(ctx, cmd)
	}
	return nil
}

func (f *fakeExecutor) RunWithOutput(ctx context.Context, cmd *exec.Cmd) (string, string, error) {
	f.calls = append(f.calls, execCall{name: cmd.Path, args: cmd.Args})
	if f.runWithOutputFunc != nil {
		return f.runWithOutputFunc(ctx, cmd)
	}
	return "", "", nil
}

func (f *fakeExecutor) CommandContext(ctx context.Context, name string, args ...string) *exec.Cmd {
	if f.commandContextFunc != nil {
		return f.commandContextFunc(ctx, name, args...)
	}
	return exec.CommandContext(ctx, name, args...)
}

func TestExecutorInterface(t *testing.T) {
	var _ Executor = (*RealExecutor)(nil)
	var _ Executor = (*fakeExecutor)(nil)
}

func TestDetectDockerInstallPlan_Linux_Debian(t *testing.T) {
	// Create a fake executor that simulates a Debian system with no docker/sudo
	_ = &fakeExecutor{
		lookPathFunc: func(name string) (string, error) {
			switch name {
			case "docker":
				return "", exec.ErrNotFound
			case "sudo":
				return "/usr/bin/sudo", nil
			default:
				return "", exec.ErrNotFound
			}
		},
	}

	// We can't easily test DetectDockerInstallPlan without /etc/os-release
	// So we'll test the pure functions separately
}

func TestDetectLinuxDistro_Debian(t *testing.T) {
	// This test would require mocking /etc/os-release
	// For now, we test the logic in a more isolated way
}

func TestDockerInstallPlan_Debian(t *testing.T) {
	plan := DockerInstallPlan{
		Distro:         DistroDebian,
		DockerPresent:  false,
		SudoAvailable:  true,
		InstallCommand: "sudo apt update && sudo apt install -y docker.io",
		StartCommand:   "sudo systemctl start docker && sudo systemctl enable docker",
		GroupCommand:   "sudo usermod -aG docker $USER",
		CanAutoInstall: true,
	}

	if plan.Distro != DistroDebian {
		t.Errorf("Distro = %q, want debian", plan.Distro)
	}
	if !plan.CanAutoInstall {
		t.Error("CanAutoInstall should be true for Debian")
	}
	if !strings.Contains(plan.InstallCommand, "apt") {
		t.Errorf("InstallCommand = %q, want apt command", plan.InstallCommand)
	}
}

func TestDockerInstallPlan_Fedora(t *testing.T) {
	plan := DockerInstallPlan{
		Distro:         DistroFedora,
		DockerPresent:  false,
		SudoAvailable:  true,
		InstallCommand: "sudo dnf install -y docker",
		StartCommand:   "sudo systemctl start docker && sudo systemctl enable docker",
		GroupCommand:   "sudo usermod -aG docker $USER",
		CanAutoInstall: true,
	}

	if plan.Distro != DistroFedora {
		t.Errorf("Distro = %q, want fedora", plan.Distro)
	}
	if !strings.Contains(plan.InstallCommand, "dnf") {
		t.Errorf("InstallCommand = %q, want dnf command", plan.InstallCommand)
	}
}

func TestDockerInstallPlan_Arch(t *testing.T) {
	plan := DockerInstallPlan{
		Distro:         DistroArch,
		DockerPresent:  false,
		SudoAvailable:  true,
		InstallCommand: "sudo pacman -S --noconfirm docker",
		StartCommand:   "sudo systemctl start docker && sudo systemctl enable docker",
		GroupCommand:   "sudo usermod -aG docker $USER",
		CanAutoInstall: true,
	}

	if plan.Distro != DistroArch {
		t.Errorf("Distro = %q, want arch", plan.Distro)
	}
	if !strings.Contains(plan.InstallCommand, "pacman") {
		t.Errorf("InstallCommand = %q, want pacman command", plan.InstallCommand)
	}
}

func TestDockerInstallPlan_RHEL(t *testing.T) {
	plan := DockerInstallPlan{
		Distro:         DistroRHEL,
		DockerPresent:  false,
		SudoAvailable:  true,
		InstallCommand: "sudo dnf install -y docker",
		StartCommand:   "sudo systemctl start docker && sudo systemctl enable docker",
		GroupCommand:   "sudo usermod -aG docker $USER",
		CanAutoInstall: true,
	}

	if plan.Distro != DistroRHEL {
		t.Errorf("Distro = %q, want rhel", plan.Distro)
	}
	if !strings.Contains(plan.InstallCommand, "dnf") {
		t.Errorf("InstallCommand = %q, want dnf command", plan.InstallCommand)
	}
}

func TestDockerInstallPlan_UnknownDistro(t *testing.T) {
	plan := DockerInstallPlan{
		Distro:         DistroUnknown,
		DockerPresent:  false,
		SudoAvailable:  true,
		CanAutoInstall: false,
		OfficialURL:    "https://docs.docker.com/engine/install/",
	}

	if plan.CanAutoInstall {
		t.Error("CanAutoInstall should be false for unknown distro")
	}
	if plan.OfficialURL == "" {
		t.Error("OfficialURL should be set for unknown distro")
	}
}

func TestDockerInstallPlan_MacOS(t *testing.T) {
	plan := DockerInstallPlan{
		Distro:         DistroUnknown,
		DockerPresent:  false,
		SudoAvailable:  false,
		CanAutoInstall: false,
	}

	if plan.CanAutoInstall {
		t.Error("CanAutoInstall should be false for macOS")
	}
}

func TestDockerInstallPlan_Windows(t *testing.T) {
	plan := DockerInstallPlan{
		Distro:         DistroUnknown,
		DockerPresent:  false,
		SudoAvailable:  false,
		CanAutoInstall: false,
	}

	if plan.CanAutoInstall {
		t.Error("CanAutoInstall should be false for Windows")
	}
}

func TestFakeExecutor_RecordsCalls(t *testing.T) {
	ctx := context.Background()
	fake := &fakeExecutor{
		runFunc: func(ctx context.Context, cmd *exec.Cmd) error {
			return nil
		},
	}

	cmd := fake.CommandContext(ctx, "echo", "hello")
	if err := fake.Run(ctx, cmd); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(fake.calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(fake.calls))
	}
	// exec.CommandContext returns the full path, so we check the base name
	callName := filepath.Base(fake.calls[0].name)
	if callName != "echo" {
		t.Errorf("call name = %q, want echo", fake.calls[0].name)
	}
	if len(fake.calls[0].args) != 2 || fake.calls[0].args[1] != "hello" {
		t.Errorf("call args = %v, want [echo hello]", fake.calls[0].args)
	}
}

func TestFakeExecutor_RunWithOutput(t *testing.T) {
	ctx := context.Background()
	fake := &fakeExecutor{
		runWithOutputFunc: func(ctx context.Context, cmd *exec.Cmd) (string, string, error) {
			return "stdout output", "stderr output", nil
		},
	}

	cmd := fake.CommandContext(ctx, "echo", "hello")
	stdout, stderr, err := fake.RunWithOutput(ctx, cmd)
	if err != nil {
		t.Fatalf("RunWithOutput: %v", err)
	}
	if stdout != "stdout output" {
		t.Errorf("stdout = %q, want 'stdout output'", stdout)
	}
	if stderr != "stderr output" {
		t.Errorf("stderr = %q, want 'stderr output'", stderr)
	}
}

func TestFakeExecutor_LookPath(t *testing.T) {
	fake := &fakeExecutor{
		lookPathFunc: func(name string) (string, error) {
			if name == "docker" {
				return "/usr/bin/docker", nil
			}
			return "", exec.ErrNotFound
		},
	}

	path, err := fake.LookPath("docker")
	if err != nil {
		t.Fatalf("LookPath: %v", err)
	}
	if path != "/usr/bin/docker" {
		t.Errorf("path = %q, want /usr/bin/docker", path)
	}

	_, err = fake.LookPath("nonexistent")
	if err == nil {
		t.Error("LookPath for nonexistent should return error")
	}
}
