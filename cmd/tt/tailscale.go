package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
)

// tailscaleStatus is the subset of `tailscale status --json` this command
// needs. CertDomains is the authoritative signal that HTTPS is enabled for the
// node; BackendState distinguishes a signed-in tailnet from a login prompt.
type tailscaleStatus struct {
	BackendState string `json:"BackendState"`
	Self         struct {
		DNSName  string `json:"DNSName"`
		HostName string `json:"HostName"`
	} `json:"Self"`
	MagicDNSSuffix string   `json:"MagicDNSSuffix"`
	CertDomains    []string `json:"CertDomains"`
}

func parseTailscaleStatus(output string) (tailscaleStatus, error) {
	var status tailscaleStatus
	if err := json.Unmarshal([]byte(output), &status); err != nil {
		return status, fmt.Errorf("decode tailscale status: %w", err)
	}
	return status, nil
}

// httpsName returns the node name Funnel/Serve serves. DNSName is the canonical
// name; CertDomains covers a status shape that omits it. A name with a scheme,
// path, port, or whitespace is never used as a URL host.
func (s tailscaleStatus) httpsName() string {
	candidates := []string{s.Self.DNSName}
	if s.Self.HostName != "" && s.MagicDNSSuffix != "" {
		candidates = append(candidates, s.Self.HostName+"."+s.MagicDNSSuffix)
	}
	candidates = append(candidates, s.CertDomains...)
	for _, candidate := range candidates {
		if name := normalizeTailscaleName(candidate); name != "" {
			return name
		}
	}
	return ""
}

func normalizeTailscaleName(name string) string {
	name = strings.TrimSuffix(strings.TrimSpace(name), ".")
	if name == "" || strings.HasPrefix(name, ".") {
		return ""
	}
	if strings.ContainsAny(name, " /:") || !strings.Contains(name, ".") {
		return ""
	}
	return name
}

// tailscaleExposeArgs keeps the Funnel/Serve choice in one place. Both verbs
// map the public or tailnet HTTPS port 443 to the server's loopback publish.
func tailscaleExposeArgs(tailnetOnly bool, port int) []string {
	verb := "funnel"
	if tailnetOnly {
		verb = "serve"
	}
	return []string{verb, "--bg", "443", fmt.Sprintf("http://127.0.0.1:%d", port)}
}

func tailscaleModeName(tailnetOnly bool) string {
	if tailnetOnly {
		return "Serve"
	}
	return "Funnel"
}

func tailscaleResetCommand(tailnetOnly bool) string {
	if tailnetOnly {
		return "tailscale serve reset"
	}
	return "tailscale funnel reset"
}

// tailscaleInstallHint prints the command rather than running it. The CLI never
// installs host software or starts a login flow on its own.
func tailscaleInstallHint(goos string) string {
	switch goos {
	case "linux":
		return "Install Tailscale with `curl -fsSL https://tailscale.com/install.sh | sh` (or your distribution's package), then run `tailscale up`."
	case "darwin":
		return "Install the Tailscale app from https://tailscale.com/download/mac (or `brew install tailscale`), then run `tailscale up`."
	case "windows":
		return "Install Tailscale with `winget install --id Tailscale.Tailscale -e` (or from https://tailscale.com/download/windows), then run `tailscale up`."
	default:
		return "Install Tailscale from https://tailscale.com/download, then run `tailscale up`."
	}
}

func lookTailscale() (string, error) {
	return exec.LookPath("tailscale")
}

// runTailscaleCommand is the production adapter. The CLI is already known to be
// on PATH; stderr is folded into the error so a Funnel ACL refusal is visible.
func runTailscaleCommand(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "tailscale", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = strings.TrimSpace(stdout.String())
		}
		if detail != "" {
			return "", fmt.Errorf("tailscale %s: %w: %s", strings.Join(args, " "), err, detail)
		}
		return "", fmt.Errorf("tailscale %s: %w", strings.Join(args, " "), err)
	}
	return stdout.String(), nil
}
