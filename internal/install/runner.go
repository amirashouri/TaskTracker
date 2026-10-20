package install

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Runner executes one step. The plan is tested against a fake; ExecRunner is
// the only implementation that touches Docker, the filesystem, or the network.
type Runner interface {
	Run(ctx context.Context, step Step) error
}

// commandRunner is the exec seam. Production uses exec.CommandContext; tests
// record the argv without requiring a Docker daemon.
type commandRunner interface {
	Run(ctx context.Context, stdout, stderr io.Writer, name string, args ...string) error
}

type execCommandRunner struct{}

func (execCommandRunner) Run(ctx context.Context, stdout, stderr io.Writer, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	return cmd.Run()
}

// ExecRunner is the production adapter. Stdout and Stderr are the CLI's
// streams so `docker compose logs -f` behaves like the command an operator
// would type by hand.
type ExecRunner struct {
	Stdout       io.Writer
	Stderr       io.Writer
	WaitTimeout  time.Duration
	PollInterval time.Duration

	commands commandRunner
}

func (r ExecRunner) Run(ctx context.Context, step Step) error {
	switch step.Kind {
	case StepWriteState:
		return SaveState(step.State)
	case StepWriteCompose:
		return writeFileAtomic(step.State.ComposePath, []byte(step.Compose), 0o600)
	case StepComposeUp:
		return r.compose(ctx, step.State, "up", "-d")
	case StepWaitReady:
		return r.waitReady(ctx, step.State)
	case StepCreateAdmin:
		return r.createAdmin(ctx, step)
	case StepComposePS:
		return r.compose(ctx, step.State, "ps")
	case StepComposeLogs:
		return r.compose(ctx, step.State, "logs", "-f", "--tail=200")
	case StepComposeDown:
		return r.compose(ctx, step.State, "down", "--remove-orphans")
	case StepRemoveState:
		if err := os.Remove(step.State.Path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove install state: %w", err)
		}
		return nil
	default:
		return fmt.Errorf("unknown install step %q", step.Kind)
	}
}

func (r ExecRunner) compose(ctx context.Context, state State, args ...string) error {
	return r.composeDisplay(ctx, state, strings.Join(args, " "), args...)
}

// composeDisplay separates the argv from what an error may print. The admin
// bootstrap carries a password in argv; redactAdminArgs replaces it before the
// command is ever formatted.
func (r ExecRunner) composeDisplay(ctx context.Context, state State, display string, args ...string) error {
	command := []string{"compose", "--project-name", state.InstanceName, "-f", state.ComposePath}
	command = append(command, args...)

	runner := r.commands
	if runner == nil {
		runner = execCommandRunner{}
	}
	if err := runner.Run(ctx, r.Stdout, r.Stderr, "docker", command...); err != nil {
		return fmt.Errorf("docker compose %s: %w", display, err)
	}
	return nil
}

func (r ExecRunner) createAdmin(ctx context.Context, step Step) error {
	if step.Admin == nil {
		return errors.New("create admin: no admin details")
	}
	if strings.TrimSpace(step.Admin.Email) == "" || step.Admin.Password == "" {
		return errors.New("create admin: email and password are required")
	}

	args := []string{
		"exec", "-T", "server", "/app/server", "user-create",
		"--email", step.Admin.Email,
		"--password", step.Admin.Password,
	}
	if step.Admin.DisplayName != "" {
		args = append(args, "--display-name", step.Admin.DisplayName)
	}
	args = append(args, "--admin")

	return r.composeDisplay(ctx, step.State, strings.Join(redactAdminArgs(args), " "), args...)
}

func redactAdminArgs(args []string) []string {
	redacted := append([]string(nil), args...)
	for i := 0; i+1 < len(redacted); i++ {
		if redacted[i] == "--password" {
			redacted[i+1] = "[redacted]"
		}
	}
	return redacted
}

// waitReady polls the health endpoint, not compose. `docker compose up -d`
// returns long before the healthcheck passes, and a CLI that prints "ready"
// early is the failure every customer hits.
func (r ExecRunner) waitReady(ctx context.Context, state State) error {
	timeout := r.WaitTimeout
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	poll := r.PollInterval
	if poll <= 0 {
		poll = time.Second
	}

	address := fmt.Sprintf("http://127.0.0.1:%d/readyz", state.ServerPort)
	client := &http.Client{Timeout: 3 * time.Second}
	deadline := time.Now().Add(timeout)

	for {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
		if err == nil {
			response, requestErr := client.Do(request)
			if requestErr == nil {
				response.Body.Close()
				if response.StatusCode == http.StatusOK {
					return nil
				}
			}
		}

		if time.Now().After(deadline) {
			return fmt.Errorf("server did not become ready at %s within %s", address, timeout)
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(poll):
		}
	}
}
