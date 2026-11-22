package main

import (
	"fmt"
	"io"

	"github.com/amirashouri/tasktracker-server/internal/install"
	"github.com/amirashouri/tasktracker-server/internal/pairing"
)

// printPairing prints the pairing link and a scannable terminal QR, or names
// the command that can produce a verified URL.
//
// A QR code of a loopback or detected-LAN address would be a phone that cannot
// connect, so there is no "best effort" here: no verified URL, no link. T9.8+
// pass the URL they verified; `tt install` in this task passes "" because no
// provider has verified one yet, which is why the hint is the real output.
func printPairing(w io.Writer, provider install.Provider, verifiedURL string) {
	if err := pairing.Print(w, verifiedURL); err != nil {
		fmt.Fprintln(w, "Pairing: no verified server URL yet.")
		fmt.Fprintf(
			w,
			"Run \"tt expose --provider=%s\" to publish one; it prints the pairing link and QR code.\n",
			pairingProvider(provider),
		)
	}
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
