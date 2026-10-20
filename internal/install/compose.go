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
	ImageTag         string
	ServerPort       int
	PublishHost      string
	DatabaseURL      string
	JWTSecret        string
	DatabasePassword string
	Postgres         bool
	Sqlite           bool
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

	data := composeData{
		ImageTag:         state.ImageTag,
		ServerPort:       state.ServerPort,
		PublishHost:      publishHost,
		DatabaseURL:      databaseURL(state),
		JWTSecret:        state.JWTSecret,
		DatabasePassword: state.DatabasePassword,
		Postgres:         state.Mode == ModePostgres,
		Sqlite:           state.Mode == ModeSQLite,
	}

	var rendered bytes.Buffer
	if err := composeTemplate.Execute(&rendered, data); err != nil {
		return "", fmt.Errorf("render compose: %w", err)
	}
	return rendered.String(), nil
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
