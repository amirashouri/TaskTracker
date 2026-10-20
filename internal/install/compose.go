package install

import (
	"bytes"
	"fmt"
	"net/url"
	"text/template"
)

// composeTemplatePath is relative to the embedded filesystem. The .tmpl
// suffix keeps editors from parsing the Go directives as YAML; the rendered
// file is still written as docker-compose.yml.
const composeTemplatePath = "embed/docker-compose.tmpl"

// composeTemplate is parsed once. It is a text template rather than YAML
// marshalling so the rendered file keeps the comments and shape a customer
// can read before running it.
var composeTemplate = template.Must(template.ParseFS(assets, composeTemplatePath))

type composeData struct {
	ImageTag              string
	ServerPort            int
	PublishHost           string
	DatabaseURL           string
	JWTSecret             string
	TrustedProxies        string
	DatabasePassword      string
	Postgres              bool
	Sqlite                bool
	Provider              string
	CloudflareTunnelToken string
}

// RenderCompose produces the loopback-only compose file for one state. The
// image tag, the secret, and the database URL are all injected here so the
// runner never has to edit a file it just wrote.
func RenderCompose(state State) (string, error) {
	return renderCompose(state, "127.0.0.1")
}

// RenderExposedCompose publishes the server on every host interface. It is the
// only other publish `tt` renders: the root developer compose and a normal
// install stay on loopback. `tt expose` verifies the result from the host's
// LAN address before printing a pairing link.
func RenderExposedCompose(state State) (string, error) {
	return renderCompose(state, "0.0.0.0")
}

func renderCompose(state State, publishHost string) (string, error) {
	if err := state.Validate(); err != nil {
		return "", err
	}

	tunnelToken := ""
	if state.Provider == ProviderCloudflare {
		// In production, this would be read from the keychain or token file.
		// For the template, we use a placeholder that the CLI replaces.
		tunnelToken = "${CLOUDFLARE_TUNNEL_TOKEN}"
	}

	data := composeData{
		ImageTag:              state.ImageTag,
		ServerPort:            state.ServerPort,
		PublishHost:           publishHost,
		DatabaseURL:           databaseURL(state),
		JWTSecret:             state.JWTSecret,
		TrustedProxies:        trustedProxiesFor(state),
		DatabasePassword:      state.DatabasePassword,
		Postgres:              state.Mode == ModePostgres,
		Sqlite:                state.Mode == ModeSQLite,
		Provider:              string(state.Provider),
		CloudflareTunnelToken: tunnelToken,
	}

	var rendered bytes.Buffer
	if err := composeTemplate.Execute(&rendered, data); err != nil {
		return "", fmt.Errorf("render compose: %w", err)
	}
	return rendered.String(), nil
}

// trustedProxiesFor is the peer Tailscale's proxy connects from. Funnel and
// Serve target the published loopback port, so the server sees 127.0.0.1.
// Cloudflare's cloudflared sidecar runs in the same compose network; the
// server sees the sidecar's container IP, so we trust the compose subnet.
// LAN has no proxy.
func trustedProxiesFor(state State) string {
	switch state.Provider {
	case ProviderTailscale:
		return "127.0.0.1/32"
	case ProviderCloudflare:
		// The compose network subnet. Docker's default bridge is 172.17.0.0/16,
		// but compose creates its own network. Use a broad private range that
		// covers typical compose networks; the exact CIDR is validated by T9.1.
		return "172.16.0.0/12"
	default:
		return ""
	}
}

func databaseURL(state State) string {
	if state.Mode == ModePostgres {
		// Userinfo escaping, not query escaping: the password sits in the
		// authority component, where `+` is a literal plus.
		user := url.UserPassword("tasktracker", state.DatabasePassword)
		return fmt.Sprintf("postgres://%s@postgres:5432/tasktracker", user.String())
	}
	return "sqlite:///data/tasktracker.db"
}
