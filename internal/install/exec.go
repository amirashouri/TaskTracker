package install

import (
	"bytes"
	"context"
	"os/exec"
)

// Executor runs commands. The production implementation wraps os/exec; tests
// provide a fake that records calls and returns scripted results.
type Executor interface {
	Run(ctx context.Context, cmd *exec.Cmd) error
	RunWithOutput(ctx context.Context, cmd *exec.Cmd) (stdout, stderr string, err error)
	LookPath(name string) (string, error)
	CommandContext(ctx context.Context, name string, args ...string) *exec.Cmd
}

// RealExecutor is the production implementation using os/exec.
type RealExecutor struct{}

func (RealExecutor) Run(ctx context.Context, cmd *exec.Cmd) error {
	return cmd.Run()
}

func (RealExecutor) RunWithOutput(ctx context.Context, cmd *exec.Cmd) (stdout, stderr string, err error) {
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	err = cmd.Run()
	return outBuf.String(), errBuf.String(), err
}

func (RealExecutor) LookPath(name string) (string, error) {
	return exec.LookPath(name)
}

func (RealExecutor) CommandContext(ctx context.Context, name string, args ...string) *exec.Cmd {
	return exec.CommandContext(ctx, name, args...)
}
