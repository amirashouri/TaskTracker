package main

import (
	"bytes"
	"net/url"
	"strings"
	"testing"

	"github.com/amirashouri/tasktracker-server/internal/install"
)

func TestConnectLinkEscapesTheWholeURL(t *testing.T) {
	serverURL := "https://tasks.example.com/a%2Fb+c?x=1&y=2#frag"

	link, err := connectLink(serverURL)
	if err != nil {
		t.Fatalf("connectLink(%q) = %v", serverURL, err)
	}

	const want = "tasktracker://connect?url=https%3A%2F%2Ftasks.example.com%2Fa%252Fb%2Bc%3Fx%3D1%26y%3D2%23frag"
	if link != want {
		t.Errorf("link = %q, want %q", link, want)
	}
	if strings.ContainsAny(link, "&#") {
		t.Errorf("link = %q, want every URL delimiter escaped", link)
	}

	// The client decodes the value exactly once. Decoding it here with the same
	// one-pass semantics proves the round trip; a second decode would turn the
	// `%25` back into `%` and lose the URL's own escape.
	parsed, err := url.Parse(link)
	if err != nil {
		t.Fatalf("parse link: %v", err)
	}
	if got := parsed.Query().Get("url"); got != serverURL {
		t.Errorf("decoded url = %q, want %q", got, serverURL)
	}
}

func TestConnectLinkAcceptsAnOrdinaryServerURL(t *testing.T) {
	for _, serverURL := range []string{
		"https://tasks.example.com",
		"https://tasks.example.com:8443",
		"http://192.168.1.50:8080",
		"http://10.0.2.2:8080/",
	} {
		t.Run(serverURL, func(t *testing.T) {
			if _, err := connectLink(serverURL); err != nil {
				t.Errorf("connectLink(%q) = %v, want a link", serverURL, err)
			}
		})
	}
}

func TestConnectLinkRefusesAValueThatCannotBeAServerURL(t *testing.T) {
	for _, tc := range []struct {
		name      string
		serverURL string
	}{
		{"empty", ""},
		{"whitespace only", " \t\n"},
		{"no scheme", "tasks.example.com"},
		{"unsupported scheme", "ftp://tasks.example.com"},
		{"missing host", "https://"},
		{"interior space", "https://tasks.example.com/a b"},
		{"interior control character", "https://tasks.example.com/a\nb"},
		{"malformed escape", "https://tasks.example.com/%zz"},
		{"too long for a QR code", "https://tasks.example.com/" + strings.Repeat("a", 2000)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			link, err := connectLink(tc.serverURL)
			if err == nil {
				t.Fatalf("connectLink(%q) = %q, want an error", tc.serverURL, link)
			}
			if link != "" {
				t.Errorf("link = %q with an error, want empty", link)
			}
		})
	}
}

func TestPrintPairingPrintsTheLinkAndQR(t *testing.T) {
	var out bytes.Buffer

	printPairing(&out, install.ProviderTailscale, "https://tasks.example.com")

	text := out.String()
	wantLink := "Pairing link: tasktracker://connect?url=https%3A%2F%2Ftasks.example.com"
	if !strings.Contains(text, wantLink) {
		t.Errorf("output = %q, want %q", text, wantLink)
	}
	// Half-block QR output. The exact raster is the library's, but a printer
	// that forgot to render the code would leave only the link line.
	if !strings.ContainsAny(text, "▄▀█") {
		t.Errorf("output = %q, want a QR made of half blocks", text)
	}
	if strings.Contains(text, "tt expose") {
		t.Errorf("output = %q, want no expose hint when a URL is verified", text)
	}
}

func TestPrintPairingWithoutAVerifiedURLNamesExpose(t *testing.T) {
	for _, tc := range []struct {
		name     string
		provider install.Provider
		want     string
	}{
		{"lan", install.ProviderLAN, "tt expose --provider=lan"},
		{"tailscale", install.ProviderTailscale, "tt expose --provider=tailscale"},
		{"unknown falls back to the default", install.Provider(""), "tt expose --provider=lan"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer

			printPairing(&out, tc.provider, "")

			text := out.String()
			if !strings.Contains(text, tc.want) {
				t.Errorf("output = %q, want %q", text, tc.want)
			}
			if strings.Contains(text, "tasktracker://") {
				t.Errorf("output = %q, want no pairing link without a verified URL", text)
			}
			if strings.ContainsAny(text, "▄▀█") {
				t.Errorf("output = %q, want no QR without a verified URL", text)
			}
		})
	}
}
