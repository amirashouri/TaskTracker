package main

import (
	"errors"
	"fmt"
	"io"
	"net/mail"
	"path/filepath"
	"strings"

	"github.com/amirashouri/tasktracker-server/internal/install"
)

func validateInstallShape(opts installOptions, buildVersion string) error {
	switch install.Mode(opts.mode) {
	case install.ModeSQLite, install.ModePostgres:
	default:
		return fmt.Errorf("mode %q is not sqlite or postgres", opts.mode)
	}
	switch install.Provider(opts.provider) {
	case install.ProviderLAN, install.ProviderTailscale, install.ProviderCloudflare:
	default:
		return fmt.Errorf("provider %q is not lan, tailscale, or cloudflare", opts.provider)
	}
	if strings.TrimSpace(opts.name) == "" {
		return errors.New("an instance name is required")
	}
	if opts.port < 1 || opts.port > 65535 {
		return fmt.Errorf("port %d is not between 1 and 65535", opts.port)
	}
	if strings.TrimSpace(buildVersion) == "" {
		return errors.New("a build version is required to pin the server image")
	}
	return nil
}

func normalizeDataDir(dataDir string) (string, error) {
	if strings.TrimSpace(dataDir) == "" {
		return "", errors.New("a data directory is required; pass --data-dir")
	}
	absolute, err := filepath.Abs(dataDir)
	if err != nil {
		return "", fmt.Errorf("resolve data directory %s: %w", dataDir, err)
	}
	return absolute, nil
}

func validateAdminFlags(opts installOptions) error {
	email := strings.TrimSpace(opts.adminEmail)
	if email == "" || opts.adminPassword == "" {
		return errors.New("no admin credentials for a non-interactive install; pass --admin-email and --admin-password (or run in a terminal without --yes)")
	}
	if !validEmail(email) {
		return fmt.Errorf("admin email %q is not valid", email)
	}
	if len(opts.adminPassword) < minAdminPasswordLength {
		return fmt.Errorf("admin password must be at least %d characters", minAdminPasswordLength)
	}
	return nil
}

func askMode(p prompter, out io.Writer, defaultValue string) (install.Mode, error) {
	for {
		raw, err := p.Ask("Database [sqlite/postgres]", defaultValue)
		if err != nil {
			return "", err
		}
		switch mode := install.Mode(strings.ToLower(strings.TrimSpace(raw))); mode {
		case install.ModeSQLite, install.ModePostgres:
			return mode, nil
		default:
			fmt.Fprintln(out, "Choose sqlite or postgres.")
		}
	}
}

func askDataDir(p prompter, defaultValue string) (string, error) {
	var raw string
	var err error
	if strings.TrimSpace(defaultValue) == "" {
		raw, err = p.AskRequired("Data directory")
	} else {
		raw, err = p.Ask("Data directory", defaultValue)
	}
	if err != nil {
		return "", err
	}
	return normalizeDataDir(raw)
}

func askAdmin(p prompter, out io.Writer) (*install.AdminBootstrap, error) {
	email, err := askEmail(p, out)
	if err != nil {
		return nil, err
	}
	displayName, err := p.Ask("Admin display name (optional)", "")
	if err != nil {
		return nil, err
	}

	for {
		password, err := p.Secret("Admin password")
		if err != nil {
			return nil, err
		}
		if len(password) < minAdminPasswordLength {
			fmt.Fprintf(out, "Use at least %d characters.\n", minAdminPasswordLength)
			continue
		}
		confirm, err := p.Secret("Confirm admin password")
		if err != nil {
			return nil, err
		}
		if password != confirm {
			fmt.Fprintln(out, "Passwords do not match.")
			continue
		}
		return &install.AdminBootstrap{
			Email:       email,
			Password:    password,
			DisplayName: strings.TrimSpace(displayName),
		}, nil
	}
}

func askEmail(p prompter, out io.Writer) (string, error) {
	for {
		email, err := p.AskRequired("Admin email")
		if err != nil {
			return "", err
		}
		email = strings.TrimSpace(email)
		if validEmail(email) {
			return email, nil
		}
		fmt.Fprintln(out, "Enter an email address like admin@example.com.")
	}
}

func validEmail(email string) bool {
	address, err := mail.ParseAddress(email)
	return err == nil && address.Address == email
}

func printLAN(w io.Writer, state install.State, lanAddress func() string) {
	if state.Provider != install.ProviderLAN {
		fmt.Fprintf(w, "Provider %s is not implemented in this build; the server is reachable locally only.\n", state.Provider)
		return
	}

	if lanAddress != nil {
		if ip := lanAddress(); ip != "" {
			fmt.Fprintf(w, "LAN address: %s\n", ip)
		} else {
			fmt.Fprintln(w, "LAN address: not detected; connect this host to the network before exposing it.")
		}
	}
	fmt.Fprintln(w, "If this host is a laptop, keep it plugged in, on Ethernet, and with the lid open: a laptop that sleeps is a server that is down.")
}
