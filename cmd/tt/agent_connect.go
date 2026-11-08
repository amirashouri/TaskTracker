package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"

	"github.com/amirashouri/tasktracker-server/internal/runner"
)

func runAgentConnect(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("agent connect", flag.ContinueOnError)
	fs.SetOutput(stderr)
	server := fs.String("server", "", "TaskTracker server URL")
	email := fs.String("email", "", "account email")
	name := fs.String("name", "", "this machine's name (default: hostname)")
	provider := fs.String("provider", "echo", "echo, openai-compatible or anthropic")
	model := fs.String("model", "", "model name, for a provider that needs one")
	baseURL := fs.String("base-url", "", "model endpoint, stored with the provider")
	apiKey := fs.String("api-key", "", "provider API key, stored in the 0600 config (TT_AGENT_API_KEY also works)")
	pairingCode := fs.String("pairing-code", "", "redeem a client-issued ttpc_… pairing code instead of signing in with email and password")
	force := fs.Bool("force", false, "replace an existing connection")
	if code, done := parseFlags(fs, args, stderr); done {
		return code
	}

	// Visit sees only flags the caller actually supplied, so an explicit empty
	// code is an error rather than a silent fall-through to the password prompt.
	pairingCodeGiven := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "pairing-code" {
			pairingCodeGiven = true
		}
	})
	code := strings.TrimSpace(*pairingCode)
	if pairingCodeGiven && code == "" {
		fmt.Fprintln(stderr, "tt agent connect: --pairing-code needs a ttpc_… code")
		return 2
	}
	if code != "" && strings.TrimSpace(*email) != "" {
		fmt.Fprintln(stderr, "tt agent connect: --pairing-code replaces --email; pass one or the other, not both")
		return 2
	}

	configPath, err := runner.DefaultConfigPath()
	if err != nil {
		return fail(ctx, stderr, "tt agent connect", err)
	}
	if _, err := runner.LoadConfig(configPath); err == nil && !*force {
		fmt.Fprintf(stderr, "tt agent connect: already connected (%s); run tt agent disconnect first, or pass --force\n", configPath)
		return 1
	}

	providerName := strings.TrimSpace(*provider)
	if providerName == "" {
		providerName = runner.ProviderEcho
	}
	if !runner.KnownProvider(providerName) {
		fmt.Fprintf(stderr, "tt agent connect: unknown provider %q; this build supports %s\n", providerName, runner.ProviderList)
		return 2
	}
	if providerName != runner.ProviderEcho && strings.TrimSpace(*model) == "" {
		fmt.Fprintf(stderr, "tt agent connect: provider %s needs --model\n", providerName)
		return 2
	}
	if providerName == runner.ProviderAnthropic && firstNonEmpty(*apiKey, os.Getenv("TT_AGENT_API_KEY")) == "" {
		fmt.Fprintln(stderr, "tt agent connect: provider anthropic needs --api-key or TT_AGENT_API_KEY")
		return 2
	}

	p := newPrompter(ctx, stdin, stdout)
	serverURL := strings.TrimSpace(*server)
	if serverURL == "" {
		serverURL, err = p.AskRequired("Server URL (for example http://192.168.1.20:8080)")
		if err != nil {
			return fail(ctx, stderr, "tt agent connect", err)
		}
	}
	serverURL, err = normaliseServerURL(serverURL)
	if err != nil {
		fmt.Fprintf(stderr, "tt agent connect: %v\n", err)
		return 2
	}

	machineName := strings.TrimSpace(*name)
	if machineName == "" {
		if host, hostErr := os.Hostname(); hostErr == nil && strings.TrimSpace(host) != "" {
			machineName = host
		} else {
			machineName = "agent"
		}
	}
	modelLabel := strings.TrimSpace(*model)

	// Both paths end with the same runner token in the config, so `tt agent run`
	// cannot tell how the machine was paired. The difference is the bootstrap
	// credential: a one-use code that already carries the project assignment, or
	// a member's email and password used once to register a machine-wide runner.
	var registration runner.Registration
	if code != "" {
		// No email, no password: the code is the credential for this one call and
		// the server spends it. An invalid, spent or expired code gets the same
		// 401, so the CLI does not try to classify it locally.
		registration, err = runner.Pair(ctx, serverURL, code, machineName, providerName, modelLabel)
	} else {
		address := strings.TrimSpace(*email)
		if address == "" {
			address, err = p.AskRequired("Email")
			if err != nil {
				return fail(ctx, stderr, "tt agent connect", err)
			}
		}
		// The password is used once and never written: the runner token is the only
		// credential this machine keeps.
		password, passwordErr := p.Secret("Password")
		if passwordErr != nil {
			return fail(ctx, stderr, "tt agent connect", passwordErr)
		}
		accessToken, loginErr := runner.Login(ctx, serverURL, address, password)
		if loginErr != nil {
			return fail(ctx, stderr, "tt agent connect", loginErr)
		}
		registration, err = runner.Register(ctx, serverURL, accessToken, machineName, providerName, modelLabel)
	}
	if err != nil {
		return fail(ctx, stderr, "tt agent connect", err)
	}

	cfg := runner.Config{
		ServerURL:  serverURL,
		RunnerID:   registration.RunnerID,
		RunnerName: registration.Name,
		Token:      registration.Token,
		Provider:   providerName,
		Model:      modelLabel,
		BaseURL:    strings.TrimRight(strings.TrimSpace(*baseURL), "/"),
		APIKey:     strings.TrimSpace(*apiKey),
	}
	if err := runner.SaveConfig(configPath, cfg); err != nil {
		return fail(ctx, stderr, "tt agent connect", err)
	}

	fmt.Fprintf(stdout, "connected %s to %s\n", cfg.RunnerName, serverURL)
	fmt.Fprintf(stdout, "runner: %s\n", cfg.RunnerID)
	fmt.Fprintf(stdout, "provider: %s\n", cfg.Provider)
	if cfg.Model != "" {
		fmt.Fprintf(stdout, "model: %s\n", cfg.Model)
	}
	if cfg.BaseURL != "" {
		fmt.Fprintf(stdout, "endpoint: %s\n", cfg.BaseURL)
	}
	fmt.Fprintf(stdout, "config: %s\n", configPath)
	fmt.Fprintln(stdout, "next: tt agent run")
	return 0
}

// normaliseServerURL trims the trailing slash and refuses anything that is not a
// plain http(s) origin. A query string or fragment is rejected rather than
// silently dropped: either would change the address the user believes they paired.
func normaliseServerURL(raw string) (string, error) {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("server URL %q is not parseable: %w", raw, err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("server URL %q must be http or https", raw)
	}
	if parsed.Host == "" {
		return "", fmt.Errorf("server URL %q has no host", raw)
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", fmt.Errorf("server URL %q must not carry a query or fragment", raw)
	}
	return raw, nil
}
