package install

import (
	"context"
	"fmt"
	"strings"
)

func (r ExecRunner) adminList(ctx context.Context, state State) error {
	args := []string{"admin-list"}
	if state.runtimeOrDefault() == RuntimeNative {
		if strings.TrimSpace(state.BinaryPath) == "" {
			return fmt.Errorf("native install has no server binary")
		}
		if strings.TrimSpace(state.EnvPath) == "" {
			return fmt.Errorf("native install has no env file")
		}
		args := append([]string{"ENV_FILE=" + state.EnvPath, state.BinaryPath}, "admin-list")
		return r.commandRedacted(ctx, "env "+strings.Join(args, " "), "env", args...)
	}
	argsCompose := append([]string{"exec", "-T", "server", "/app/server"}, args...)
	return r.composeDisplay(ctx, state, strings.Join(argsCompose, " "), argsCompose...)
}

func (r ExecRunner) adminResetPassword(ctx context.Context, step Step) error {
	if step.Admin == nil {
		return fmt.Errorf("admin reset: no credentials")
	}
	args := []string{"user-set-password", "--email", step.Admin.Email, "--password", step.Admin.Password}
	if step.State.runtimeOrDefault() == RuntimeNative {
		if strings.TrimSpace(step.State.BinaryPath) == "" {
			return fmt.Errorf("native install has no server binary")
		}
		if strings.TrimSpace(step.State.EnvPath) == "" {
			return fmt.Errorf("native install has no env file")
		}
		full := append([]string{"ENV_FILE=" + step.State.EnvPath, step.State.BinaryPath}, args...)
		return r.commandRedacted(ctx, "env "+strings.Join(redactAdminArgs(full), " "), "env", full...)
	}
	full := append([]string{"exec", "-T", "server", "/app/server"}, args...)
	return r.composeDisplay(ctx, step.State, strings.Join(redactAdminArgs(full), " "), full...)
}
