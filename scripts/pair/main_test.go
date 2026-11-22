package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestRunPrintsTheURLPairingLinkAndQR(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/readyz" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	var out bytes.Buffer
	if err := run(&out, server.URL, 0); err != nil {
		t.Fatalf("run: %v", err)
	}

	text := out.String()
	if want := "Exposed on the LAN: " + server.URL; !strings.Contains(text, want) {
		t.Errorf("output = %q, want %q", text, want)
	}
	if want := "Pairing link: tasktracker://connect?url=" + url.QueryEscape(server.URL); !strings.Contains(text, want) {
		t.Errorf("output = %q, want %q", text, want)
	}
	// A run that verified the URL but printed no code would still pass the two
	// assertions above, so pin the half-block raster too.
	if !strings.ContainsAny(text, "▄▀█") {
		t.Errorf("output = %q, want a QR made of half blocks", text)
	}
}
