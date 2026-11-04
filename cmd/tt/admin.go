package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/amirashouri/tasktracker-server/internal/install"
)

// runAdmin routes admin subcommands that operate against the installed instance.
func runAdmin(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer, statePath string, runner install.Runner) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "tt admin: missing subcommand (show or reset-password)")
		return 2
	}

	switch args[0] {
	case "show", "info":
		return runAdminShow(ctx, args[1:], stdout, stderr, statePath, runner)
	case "reset-password":
		return runAdminResetPassword(ctx, args[1:], stdin, stdout, stderr, statePath, runner)
	default:
		fmt.Fprintf(stderr, "tt admin: unknown subcommand %q\n", args[0])
		return 2
	}
}

func runAdminShow(ctx context.Context, args []string, stdout, stderr io.Writer, statePath string, runner install.Runner) int {
	fs := flag.NewFlagSet("admin show", flag.ContinueOnError)
	fs.SetOutput(stderr)
	if code, done := parseFlags(fs, args, stderr); done {
		return code
	}

	state, err := loadState(statePath)
	if err != nil {
		fmt.Fprintf(stderr, "tt admin show: %v\n", err)
		return 1
	}
	if state.Version == 0 {
		fmt.Fprintln(stderr, "tt admin show: no installation found; run tt install")
		return 1
	}

	cmd := install.Command{Action: install.ActionAdminList}
	steps, err := install.Next(state, cmd)
	if err != nil {
		fmt.Fprintf(stderr, "tt admin show: %v\n", err)
		return 1
	}
	return execute(ctx, steps, stderr, runner)
}

func runAdminResetPassword(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer, statePath string, runner install.Runner) int {
	fs := flag.NewFlagSet("admin reset-password", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var email string
	fs.StringVar(&email, "email", "", "admin email to reset (required)")

	if code, done := parseFlags(fs, args, stderr); done {
		return code
	}
	email = strings.TrimSpace(email)
	if email == "" {
		fmt.Fprintln(stderr, "tt admin reset-password: --email is required")
		return 2
	}

	state, err := loadState(statePath)
	if err != nil {
		fmt.Fprintf(stderr, "tt admin reset-password: %v\n", err)
		return 1
	}
	if state.Version == 0 {
		fmt.Fprintln(stderr, "tt admin reset-password: no installation found; run tt install")
		return 1
	}

	p := newPrompter(ctx, stdin, stderr)
	pass, err := p.Secret("New password")
	if err != nil {
		return fail(ctx, stderr, "tt admin reset-password", err)
	}
	if len(pass) < minAdminPasswordLength {
		fmt.Fprintf(stderr, "tt admin reset-password: password must be at least %d characters\n", minAdminPasswordLength)
		return 1
	}
	confirm, err := p.Secret("Confirm new password")
	if err != nil {
		return fail(ctx, stderr, "tt admin reset-password", err)
	}
	if pass != confirm {
		fmt.Fprintln(stderr, "tt admin reset-password: passwords do not match")
		return 1
	}

	cmd := install.Command{
		Action:        install.ActionAdminResetPassword,
		AdminEmail:    email,
		AdminPassword: pass,
	}
	steps, err := install.Next(state, cmd)
	if err != nil {
		fmt.Fprintf(stderr, "tt admin reset-password: %v\n", err)
		return 1
	}
	return execute(ctx, steps, stderr, runner)
}
