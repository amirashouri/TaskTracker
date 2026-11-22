package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/amirashouri/tasktracker-server/internal/install"
)

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
