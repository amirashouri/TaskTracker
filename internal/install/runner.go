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
	case StepComposeLogs:
		return r.compose(ctx, step.State, "logs", "-f", "--tail=200")
	case StepComposeDown:
		return r.compose(ctx, step.State, "down", "--remove-orphans")
	case StepRemoveState:
		if err := os.Remove(step.State.Path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove install state: %w", err)
		}
		return nil
	case StepEnsureUser:
		return r.ensureServiceUser(ctx, step.State)
	case StepWriteEnv:
		return writeFileAtomic(step.State.EnvPath, []byte(step.Env), 0o600)
	case StepWriteUnit:
		return writeFileAtomic(step.State.UnitPath, []byte(step.Unit), 0o644)
	case StepStartService:
		return r.startService(ctx, step.State)
	case StepRestartService:
		return r.systemctl(ctx, step.State, "restart")
	case StepStopService:
		return r.stopService(ctx, step.State)
	case StepRemoveEnv:
		if err := os.Remove(step.State.EnvPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove environment file: %w", err)
		}
		return nil
	case StepServiceLogs:
		return r.command(ctx, "journalctl", "-u", step.State.ServiceName(), "-f", "--no-pager", "-n", "200")
	case StepAdminList:
		return r.adminList(ctx, step.State)
	case StepAdminResetPassword:
		return r.adminResetPassword(ctx, step)
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
	runner := r.commands
	if runner == nil {
		runner = execCommandRunner{}
	}
	if err := runner.Run(ctx, r.Stdout, r.Stderr, "docker", composeArgs(state, args...)...); err != nil {
		return fmt.Errorf("docker compose %s: %w", display, err)
	}
	return nil
}

// composeArgs is the single compose invocation shape shared by the executing
// runner and the read-only checks.
func composeArgs(state State, args ...string) []string {
	command := []string{"compose", "--project-name", state.InstanceName, "-f", state.ComposePath}
	return append(command, args...)
}

func (r ExecRunner) createAdmin(ctx context.Context, step Step) error {
	if step.Admin == nil {
		return errors.New("create admin: no admin details")
	}
	if strings.TrimSpace(step.Admin.Email) == "" || step.Admin.Password == "" {
		return errors.New("create admin: email and password are required")
	}

	adminArgs := []string{
		"user-create",
		"--email", step.Admin.Email,
		"--password", step.Admin.Password,
	}
	if step.Admin.DisplayName != "" {
		adminArgs = append(adminArgs, "--display-name", step.Admin.DisplayName)
	}
	adminArgs = append(adminArgs, "--admin")

	if step.State.runtimeOrDefault() == RuntimeNative {
		// A native server has no container to exec into: run the binary the unit
		// runs, with the unit's env file, so both read the same database and the
		// same secret.
		args := append([]string{"ENV_FILE=" + step.State.EnvPath, step.State.BinaryPath}, adminArgs...)
		return r.commandRedacted(ctx, "env "+strings.Join(redactAdminArgs(args), " "), "env", args...)
	}

	args := append([]string{"exec", "-T", "server", "/app/server"}, adminArgs...)
	return r.composeDisplay(ctx, step.State, strings.Join(redactAdminArgs(args), " "), args...)
}

// ensureServiceUser creates the system account the unit runs as and hands it
// the data directory. Both halves are idempotent, because a repair runs them
// again on a host that already has the account.
func (r ExecRunner) ensureServiceUser(ctx context.Context, state State) error {
	if err := r.runQuiet(ctx, "id", "-u", NativeServiceUser); err != nil {
		if err := r.command(ctx, "useradd", "--system", "--home", state.DataDir, "--shell", "/usr/sbin/nologin", NativeServiceUser); err != nil {
			return fmt.Errorf("create the %s service account: %w", NativeServiceUser, err)
		}
	}
	return r.command(ctx, "chown", "-R", NativeServiceUser+":"+NativeServiceUser, state.DataDir)
}

func (r ExecRunner) startService(ctx context.Context, state State) error {
	if err := r.command(ctx, "systemctl", "daemon-reload"); err != nil {
		return err
	}
	return r.systemctl(ctx, state, "enable", "--now")
}

// stopService removes the unit only after systemd has stopped using it, so a
// failure leaves a service that can still be inspected.
func (r ExecRunner) stopService(ctx context.Context, state State) error {
	if err := r.systemctl(ctx, state, "disable", "--now"); err != nil {
		return err
	}
	if err := os.Remove(state.UnitPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove %s: %w", state.UnitPath, err)
	}
	return r.command(ctx, "systemctl", "daemon-reload")
}

// systemctl puts the unit last: `systemctl enable --now tasktracker.service`.
func (r ExecRunner) systemctl(ctx context.Context, state State, verbs ...string) error {
	return r.command(ctx, "systemctl", append(verbs, state.ServiceName())...)
}

// command runs one host command with the CLI's streams, so the native steps
// read like the commands an operator would type by hand.
func (r ExecRunner) command(ctx context.Context, name string, args ...string) error {
	runner := r.commands
	if runner == nil {
		runner = execCommandRunner{}
	}
	if err := runner.Run(ctx, r.Stdout, r.Stderr, name, args...); err != nil {
		return fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	return nil
}

// commandRedacted runs a command whose argv may carry a secret. The error names
// the redacted form, because a wrapped *exec.Cmd would otherwise put the admin
// password in a failure message.
func (r ExecRunner) commandRedacted(ctx context.Context, display, name string, args ...string) error {
	runner := r.commands
	if runner == nil {
		runner = execCommandRunner{}
	}
	if err := runner.Run(ctx, r.Stdout, r.Stderr, name, args...); err != nil {
		return fmt.Errorf("%s: %w", display, err)
	}
	return nil
}

// runQuiet is a probe: its output is noise unless the caller prints it, and a
// zero exit is a "yes".
func (r ExecRunner) runQuiet(ctx context.Context, name string, args ...string) error {
	runner := r.commands
	if runner == nil {
		runner = execCommandRunner{}
	}
	return runner.Run(ctx, io.Discard, io.Discard, name, args...)
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

// WaitReady polls address until it answers 200 or the timeout expires. The
// install runner uses it for loopback; `tt expose` uses it for the host's LAN
// address, which is the check that catches a publish that is not really
// reachable from the network.
func WaitReady(ctx context.Context, address string, timeout, poll time.Duration) error {
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	if poll <= 0 {
		poll = time.Second
	}

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

// waitReady polls the health endpoint, not compose. `docker compose up -d`
// returns long before the healthcheck passes, and a CLI that prints "ready"
// early is the failure every customer hits.
func (r ExecRunner) waitReady(ctx context.Context, state State) error {
	address := fmt.Sprintf("http://127.0.0.1:%d/readyz", state.ServerPort)
	return WaitReady(ctx, address, r.WaitTimeout, r.PollInterval)
}
