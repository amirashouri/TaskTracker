// Package pairing turns a verified server URL into the client's
// tasktracker://connect link and renders it as a terminal QR code.
//
// The link grammar is a cross-repo contract with the installed client, so the
// two paths that print it — `tt expose` and the `make expose` helper — share
// this code rather than each writing their own escape.
package pairing

import (
	"fmt"
	"io"
	"net/url"
	"strings"
	"unicode"

	"github.com/mdp/qrterminal/v3"
)

// LinkPrefix is the client's pairing URL scheme. The installed client parses
// this exact prefix, so it is part of the cross-repo contract.
const LinkPrefix = "tasktracker://connect?url="

// The QR printer encodes the link at level M, whose byte-mode ceiling is 2331
// bytes; qrterminal ignores qr.Encode's error and would panic on a nil code.
// Refusing with margin keeps a pathological URL from taking the caller down.
const maxLinkLength = 2000

// Link turns a verified server URL into the client's pairing link.
//
// The whole URL is escaped as one query value, not just its path: the client
// decodes the value exactly once, so an `&`, `#`, or `?` left unescaped would
// split the link, and a `%` left unescaped would be decoded into a different
// address. `url.QueryEscape` maps `%` to `%25`, `&` to `%26`, and `#` to `%23`,
// which is what makes that single decode reconstruct the original string.
//
// The caller is responsible for having verified the URL is reachable. This
// function only refuses a value that cannot be a server URL at all.
func Link(serverURL string) (string, error) {
	serverURL = strings.TrimSpace(serverURL)
	if serverURL == "" {
		return "", fmt.Errorf("no verified server URL")
	}
	// QueryEscape encodes a space as `+`, and the client's decoder deliberately
	// does not read `+` as a space. Refusing whitespace keeps the two halves
	// from disagreeing about an address the customer would confirm.
	if strings.IndexFunc(serverURL, unicode.IsSpace) >= 0 {
		return "", fmt.Errorf("server URL %q contains whitespace", serverURL)
	}

	parsed, err := url.Parse(serverURL)
	if err != nil {
		return "", fmt.Errorf("server URL %q is not parseable: %w", serverURL, err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("server URL %q is not http or https", serverURL)
	}
	if parsed.Host == "" {
		return "", fmt.Errorf("server URL %q has no host", serverURL)
	}

	link := LinkPrefix + url.QueryEscape(serverURL)
	if len(link) > maxLinkLength {
		return "", fmt.Errorf("server URL %q is too long for a QR code", serverURL)
	}
	return link, nil
}

// Print writes the pairing link and a scannable terminal QR for a verified
// server URL. It writes nothing when the URL cannot be a pairing target.
func Print(w io.Writer, serverURL string) error {
	link, err := Link(serverURL)
	if err != nil {
		return err
	}

	fmt.Fprintf(w, "Pairing link: %s\n", link)
	qrterminal.GenerateWithConfig(link, qrterminal.Config{
		Level:      qrterminal.M,
		Writer:     w,
		HalfBlocks: true,
	})
	return nil
}
