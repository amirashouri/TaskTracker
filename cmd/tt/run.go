package main

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// runExternal runs a host CLI and returns its stdout. stderr is folded into the
// error because that is where a refused Funnel ACL or a missing Cloudflare zone
// is explained; stdout is the fallback when stderr is empty.
func runExternal(ctx context.Context, program string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, program, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = strings.TrimSpace(stdout.String())
		}
		if detail != "" {
			return "", fmt.Errorf("%s %s: %w: %s", program, strings.Join(args, " "), err, detail)
		}
		return "", fmt.Errorf("%s %s: %w", program, strings.Join(args, " "), err)
	}
	return stdout.String(), nil
}

func runCloudflareCommand(ctx context.Context, args ...string) (string, error) {
	return runExternal(ctx, "cloudflared", args...)
}

func runTailscaleCommand(ctx context.Context, args ...string) (string, error) {
	return runExternal(ctx, "tailscale", args...)
}
