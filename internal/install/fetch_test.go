package install

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFetchBinaryDownloadsAndMakesExecutable(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "server")

	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write([]byte("#!/bin/sh\necho hi\n"))
	}))
	defer srv.Close()

	if err := FetchBinary(context.Background(), srv.Client(), srv.URL+"/server", dest); err != nil {
		t.Fatalf("FetchBinary: %v", err)
	}

	data, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("read dest: %v", err)
	}
	if !strings.Contains(string(data), "echo hi") {
		t.Fatalf("dest does not contain the payload: %q", string(data))
	}
	if info, err := os.Stat(dest); err != nil || info.Mode()&0o111 == 0 {
		t.Fatalf("dest is not executable: err=%v mode=%v", err, info.Mode())
	}
	if _, err := os.Stat(dest + ".part"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the .part file was left behind: %v", err)
	}
}

func TestFetchBinaryRefusesHTTP(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "server")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("hi"))
	}))
	defer srv.Close()

	// The test server is plain TCP, so build an http:// URL by hand and use a
	// client that does not follow redirects to it.
	err := FetchBinary(context.Background(), &http.Client{}, "http://"+srv.Listener.Addr().String()+"/server", dest)
	if err == nil {
		t.Fatal("expected an error for an http:// URL")
	}
	if !IsHTTPRefused(err) {
		t.Fatalf("expected IsHTTPRefused, got %v", err)
	}
	if _, statErr := os.Stat(dest); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("expected no file at %s, got %v", dest, statErr)
	}
}

func TestFetchBinaryRefusesNoHost(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "server")
	err := FetchBinary(context.Background(), nil, "https:///server", dest)
	if err == nil || !IsHTTPRefused(err) {
		t.Fatalf("expected an http-refused error, got %v", err)
	}
}

func TestFetchBinaryRefusesOverCap(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "server")

	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// One byte over the cap: the reader stops at MaxFetchBytes+1 and the
		// function reports the overrun.
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(make([]byte, MaxFetchBytes+1))
	}))
	defer srv.Close()

	err := FetchBinary(context.Background(), srv.Client(), srv.URL+"/server", dest)
	if err == nil {
		t.Fatal("expected an error for a response over the cap")
	}
	if !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("expected an exceeds-cap error, got %v", err)
	}
	if _, statErr := os.Stat(dest); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("expected no file at %s, got %v", dest, statErr)
	}
}

func TestFetchBinaryRefusesNonOK(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "server")

	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	err := FetchBinary(context.Background(), srv.Client(), srv.URL+"/server", dest)
	if err == nil || !strings.Contains(err.Error(), "server answered") {
		t.Fatalf("expected a non-2xx error, got %v", err)
	}
	if _, statErr := os.Stat(dest); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("expected no file at %s, got %v", dest, statErr)
	}
}

func TestFetchBinaryCreatesParentDirectory(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "nested", "deep", "server")

	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("payload"))
	}))
	defer srv.Close()

	if err := FetchBinary(context.Background(), srv.Client(), srv.URL+"/server", dest); err != nil {
		t.Fatalf("FetchBinary: %v", err)
	}
	if _, err := os.Stat(dest); err != nil {
		t.Fatalf("expected %s to exist: %v", dest, err)
	}
}

func TestBinaryExists(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "nope")
	if BinaryExists(missing) {
		t.Fatal("expected a missing binary to not exist")
	}

	bin := filepath.Join(dir, "server")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if !BinaryExists(bin) {
		t.Fatal("expected an executable file to exist")
	}

	nonexec := filepath.Join(dir, "data")
	if err := os.WriteFile(nonexec, []byte("payload"), 0o644); err != nil {
		t.Fatal(err)
	}
	if BinaryExists(nonexec) {
		t.Fatal("expected a non-executable file to not exist")
	}
}
