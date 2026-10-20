package main

import (
	"fmt"
	"io"
	"net/url"
	"strings"
	"unicode"

	"github.com/amirashouri/tasktracker-server/internal/install"
	"github.com/mdp/qrterminal/v3"
)

const connectLinkPrefix = "tasktracker://connect?url="

// The QR printer encodes the link at level M, whose byte-mode ceiling is 2331
// bytes; qrterminal ignores qr.Encode's error and would panic on a nil code.
// Refusing with margin keeps a pathological URL from taking the CLI down.
const maxConnectLinkLength = 2000

// connectLink turns a verified server URL into the client's pairing link.
//
// The whole URL is escaped as one query value, not just its path: the client
// decodes the value exactly once, so an `&`, `#`, or `?` left unescaped would
// split the link, and a `%` left unescaped would be decoded into a different
// address. `url.QueryEscape` maps `%` to `%25`, `&` to `%26`, and `#` to `%23`,
// which is what makes that single decode reconstruct the original string.
//
// The caller is responsible for having verified the URL is reachable. This
// function only refuses a value that cannot be a server URL at all.
func connectLink(serverURL string) (string, error) {
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

	link := connectLinkPrefix + url.QueryEscape(serverURL)
	if len(link) > maxConnectLinkLength {
		return "", fmt.Errorf("server URL %q is too long for a QR code", serverURL)
	}
	return link, nil
}

// printPairing prints the pairing link and a scannable terminal QR, or names
// the command that can produce a verified URL.
//
// A QR code of a loopback or detected-LAN address would be a phone that cannot
// connect, so there is no "best effort" here: no verified URL, no link. T9.8+
// pass the URL they verified; `tt install` in this task passes "" because no
// provider has verified one yet, which is why the hint is the real output.
func printPairing(w io.Writer, provider install.Provider, verifiedURL string) {
	link, err := connectLink(verifiedURL)
	if err != nil {
		fmt.Fprintln(w, "Pairing: no verified server URL yet.")
		fmt.Fprintf(
			w,
			"Run \"tt expose --provider=%s\" to publish one; it prints the pairing link and QR code.\n",
			pairingProvider(provider),
		)
		return
	}

	fmt.Fprintf(w, "Pairing link: %s\n", link)
	qrterminal.GenerateWithConfig(link, qrterminal.Config{
		Level:      qrterminal.M,
		Writer:     w,
		HalfBlocks: true,
	})
}

// pairingProvider keeps the hint actionable when the state is older than the
// provider check or otherwise empty: `lan` is the documented default.
func pairingProvider(provider install.Provider) install.Provider {
	switch provider {
	case install.ProviderLAN, install.ProviderTailscale, install.ProviderCloudflare:
		return provider
	default:
		return install.ProviderLAN
	}
}
