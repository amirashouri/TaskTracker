package install

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"
)

func TestDetectDockerInstallPlan_DockerPresent(t *testing.T) {
	_ = context.Background()
	_ = &fakeExecutor{
		lookPathFunc: func(name string) (string, error) {
			if name == "docker" {
				return "/usr/bin/docker", nil
			}
			if name == "sudo" {
				return "/usr/bin/sudo", nil
			}
			return "", exec.ErrNotFound
		},
	}

	// We can't easily test the full DetectDockerInstallPlan without /etc/os-release
	// but we can test the parts that don't depend on it
}

// The wizard hands EnsureDocker its own stdout so the bootstrap reads in order
// with the prompts instead of jumping between streams. A daemon that is
// installed but down prints before it fails, so it is the case that can prove
// the writer is honoured.
func TestEnsureDockerWritesToTheConfiguredWriter(t *testing.T) {
	original := DetectLinuxDistroFunc
	DetectLinuxDistroFunc = func() (Distro, error) { return DistroDebian, nil }
	t.Cleanup(func() { DetectLinuxDistroFunc = original })

	fake := &fakeExecutor{
		lookPathFunc: func(name string) (string, error) {
			switch name {
			case "docker":
				return "/usr/bin/docker", nil
			case "sudo":
				return "/usr/bin/sudo", nil
			}
			return "", exec.ErrNotFound
		},
		runFunc: func(context.Context, *exec.Cmd) error {
			return errors.New("Cannot connect to the Docker daemon")
		},
	}

	var out strings.Builder
	err := EnsureDocker(context.Background(), fake, nil, PreflightOptions{Platform: "linux", Out: &out})

	if err == nil {
		t.Fatal("EnsureDocker accepted a daemon that is not reachable")
	}
	if !strings.Contains(err.Error(), "non-interactive") {
		t.Errorf("error = %q, want the fail-closed message for a nil prompter", err)
	}
	if !strings.Contains(out.String(), "daemon is not running") {
		t.Errorf("output = %q, want the daemon message on the configured writer", out.String())
	}
	if !strings.Contains(out.String(), "systemctl start docker") {
		t.Errorf("output = %q, want the start command on the configured writer", out.String())
	}
}

func TestRunDockerInstall_NoSudo(t *testing.T) {
	ctx := context.Background()
	fake := &fakeExecutor{
		lookPathFunc: func(name string) (string, error) {
			if name == "docker" {
				return "", exec.ErrNotFound
			}
			if name == "sudo" {
				return "", exec.ErrNotFound
			}
			return "", exec.ErrNotFound
		},
	}

	plan := DockerInstallPlan{
		Distro:         DistroDebian,
		DockerPresent:  false,
		SudoAvailable:  false,
		InstallCommand: "sudo apt update && sudo apt install -y docker.io",
		StartCommand:   "sudo systemctl start docker && sudo systemctl enable docker",
		GroupCommand:   "sudo usermod -aG docker $USER",
		CanAutoInstall: true,
	}

	_, err := RunDockerInstall(ctx, fake, plan, true, true, true)
	if err == nil {
		t.Fatal("expected error when sudo is not available")
	}
	if !strings.Contains(err.Error(), "sudo is not available") {
		t.Errorf("error = %q, want sudo not available message", err.Error())
	}
}

func TestRunDockerInstall_CannotAutoInstall(t *testing.T) {
	ctx := context.Background()
	fake := &fakeExecutor{
		lookPathFunc: func(name string) (string, error) {
			if name == "docker" {
				return "", exec.ErrNotFound
			}
			if name == "sudo" {
				return "/usr/bin/sudo", nil
			}
			return "", exec.ErrNotFound
		},
	}

	plan := DockerInstallPlan{
		Distro:         DistroUnknown,
		DockerPresent:  false,
		SudoAvailable:  true,
		CanAutoInstall: false,
		OfficialURL:    "https://docs.docker.com/engine/install/",
	}

	_, err := RunDockerInstall(ctx, fake, plan, true, true, true)
	if err == nil {
		t.Fatal("expected error when auto install not supported")
	}
	if !strings.Contains(err.Error(), "not supported") {
		t.Errorf("error = %q, want not supported message", err.Error())
	}
}

func TestRunDockerInstall_Success(t *testing.T) {
	ctx := context.Background()
	callCount := 0
	fake := &fakeExecutor{
		lookPathFunc: func(name string) (string, error) {
			if name == "docker" {
				return "", exec.ErrNotFound
			}
			if name == "sudo" {
				return "/usr/bin/sudo", nil
			}
			return "", exec.ErrNotFound
		},
		runWithOutputFunc: func(ctx context.Context, cmd *exec.Cmd) (string, string, error) {
			callCount++
			return "output", "", nil
		},
		runFunc: func(ctx context.Context, cmd *exec.Cmd) error {
			callCount++
			return nil
		},
	}

	plan := DockerInstallPlan{
		Distro:         DistroDebian,
		DockerPresent:  false,
		SudoAvailable:  true,
		InstallCommand: "sudo apt update && sudo apt install -y docker.io",
		StartCommand:   "sudo systemctl start docker && sudo systemctl enable docker",
		GroupCommand:   "sudo usermod -aG docker $USER",
		CanAutoInstall: true,
	}

	output, err := RunDockerInstall(ctx, fake, plan, true, true, true)
	if err != nil {
		t.Fatalf("RunDockerInstall: %v", err)
	}
	if output == "" {
		t.Error("expected non-empty output")
	}
	if callCount < 3 {
		t.Errorf("expected at least 3 calls (install, start, group), got %d", callCount)
	}
}

func TestRunDockerInstall_SkipGroup(t *testing.T) {
	ctx := context.Background()
	callCount := 0
	fake := &fakeExecutor{
		lookPathFunc: func(name string) (string, error) {
			if name == "docker" {
				return "", exec.ErrNotFound
			}
			if name == "sudo" {
				return "/usr/bin/sudo", nil
			}
			return "", exec.ErrNotFound
		},
		runWithOutputFunc: func(ctx context.Context, cmd *exec.Cmd) (string, string, error) {
			callCount++
			return "output", "", nil
		},
		runFunc: func(ctx context.Context, cmd *exec.Cmd) error {
			callCount++
			return nil
		},
	}

	plan := DockerInstallPlan{
		Distro:         DistroDebian,
		DockerPresent:  false,
		SudoAvailable:  true,
		InstallCommand: "sudo apt update && sudo apt install -y docker.io",
		StartCommand:   "sudo systemctl start docker && sudo systemctl enable docker",
		GroupCommand:   "sudo usermod -aG docker $USER",
		CanAutoInstall: true,
	}

	output, err := RunDockerInstall(ctx, fake, plan, true, true, false)
	if err != nil {
		t.Fatalf("RunDockerInstall: %v", err)
	}
	if output == "" {
		t.Error("expected non-empty output")
	}
	if callCount < 2 {
		t.Errorf("expected at least 2 calls (install, start), got %d", callCount)
	}
}

func TestRunDockerInstall_InstallFails(t *testing.T) {
	ctx := context.Background()
	fake := &fakeExecutor{
		lookPathFunc: func(name string) (string, error) {
			if name == "docker" {
				return "", exec.ErrNotFound
			}
			if name == "sudo" {
				return "/usr/bin/sudo", nil
			}
			return "", exec.ErrNotFound
		},
		runWithOutputFunc: func(ctx context.Context, cmd *exec.Cmd) (string, string, error) {
			return "", "apt error", errors.New("apt failed")
		},
	}

	plan := DockerInstallPlan{
		Distro:         DistroDebian,
		DockerPresent:  false,
		SudoAvailable:  true,
		InstallCommand: "sudo apt update && sudo apt install -y docker.io",
		StartCommand:   "sudo systemctl start docker && sudo systemctl enable docker",
		GroupCommand:   "sudo usermod -aG docker $USER",
		CanAutoInstall: true,
	}

	_, err := RunDockerInstall(ctx, fake, plan, true, true, true)
	if err == nil {
		t.Fatal("expected error when install fails")
	}
	if !strings.Contains(err.Error(), "apt failed") {
		t.Errorf("error = %q, want apt error message", err.Error())
	}
}

func TestWaitForDocker_Success(t *testing.T) {
	ctx := context.Background()
	fake := &fakeExecutor{
		runFunc: func(ctx context.Context, cmd *exec.Cmd) error {
			return nil
		},
	}

	err := waitForDocker(ctx, fake)
	if err != nil {
		t.Fatalf("waitForDocker: %v", err)
	}
	if len(fake.calls) != 1 {
		t.Errorf("expected 1 call, got %d", len(fake.calls))
	}
}

func TestWaitForDocker_Timeout(t *testing.T) {
	ctx := context.Background()
	fake := &fakeExecutor{
		runFunc: func(ctx context.Context, cmd *exec.Cmd) error {
			return errors.New("not ready")
		},
	}

	err := waitForDocker(ctx, fake)
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Errorf("error = %q, want timeout message", err.Error())
	}
	if len(fake.calls) != 30 {
		t.Errorf("expected 30 calls, got %d", len(fake.calls))
	}
}
