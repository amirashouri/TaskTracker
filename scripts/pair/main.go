// Command pair prints the LAN URL, pairing link and QR code for the compose
// stack `make expose` publishes.
//
// It is deliberately not part of `tt`: that CLI describes an instance whose
// state `tt install` wrote, and a `make up` stack has none. The link grammar
// and QR rendering are shared with `tt expose` through internal/pairing, so
// the two outputs cannot drift.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"time"

	"github.com/amirashouri/tasktracker-server/internal/install"
	"github.com/amirashouri/tasktracker-server/internal/pairing"
)

const readyTimeout = 2 * time.Minute

func main() {
	port := flag.Int("port", 8080, "server port the compose stack publishes")
	serverURL := flag.String("url", "", "server URL to pair; defaults to this host's LAN address")
	flag.Parse()

	if err := run(os.Stdout, *serverURL, *port); err != nil {
		fmt.Fprintf(os.Stderr, "pair: %v\n", err)
		os.Exit(1)
	}
}

// run verifies the URL answers /readyz before printing it, exactly as
// `tt expose` does: a QR code for an address that does not respond is a phone
// that cannot connect.
func run(w io.Writer, serverURL string, port int) error {
	if serverURL == "" {
		var err error
		serverURL, err = lanURL(port)
		if err != nil {
			return err
		}
	}

	readyURL := serverURL + "/readyz"
	if err := install.WaitReady(context.Background(), readyURL, readyTimeout, time.Second); err != nil {
		return fmt.Errorf("no response from %s: %w", readyURL, err)
	}

	fmt.Fprintf(w, "Exposed on the LAN: %s\n", serverURL)
	fmt.Fprintln(w, "Plain HTTP on your local network: trust this network, and do not forward this port from the internet.")
	fmt.Fprintln(w, "`make up` returns the stack to loopback; re-run `make expose` to publish it again.")
	return pairing.Print(w, serverURL)
}

func lanURL(port int) (string, error) {
	ip := install.LANAddress()
	if ip == "" {
		return "", fmt.Errorf("no LAN address detected; connect this host to the network and try again")
	}
	parsed := net.ParseIP(ip)
	if parsed == nil || parsed.IsLoopback() || parsed.IsUnspecified() {
		return "", fmt.Errorf("detected address %q is not a LAN address; refusing to pair on loopback", ip)
	}
	return "http://" + net.JoinHostPort(ip, strconv.Itoa(port)), nil
}
