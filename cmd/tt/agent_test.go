package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/amirashouri/tasktracker-server/internal/install"
	"github.com/amirashouri/tasktracker-server/internal/runner"
)

// agentServerCalls counts which bootstrap the CLI actually used. Logging in on a
// pairing-code connect would silently pair the wrong kind of runner.
type agentServerCalls struct {
	revokes       atomic.Int32
	logins        atomic.Int32
	registrations atomic.Int32
	pairs         atomic.Int32
}

// agentServer is the routes both pairing paths need, plus the health probe. It
// checks the credentials, because storing the wrong one is the failure that only
// shows up later, at the first run.
func agentServer(t *testing.T, calls *agentServerCalls) *httptest.Server {
	t.Helper()

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("/v1/auth/login", func(w http.ResponseWriter, r *http.Request) {
		calls.logins.Add(1)
		var body struct {
			Email    string `json:"email"`
			Password string `json:"password"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode login: %v", err)
		}
		if body.Email != "dev@example.com" || body.Password != "secret" {
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid credentials"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"accessToken": "jwt"})
	})
	mux.HandleFunc("/v1/agent/runners", func(w http.ResponseWriter, r *http.Request) {
		calls.registrations.Add(1)
		if r.Header.Get("Authorization") != "Bearer jwt" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var body struct {
			Name     string `json:"name"`
			Provider string `json:"provider"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode registration: %v", err)
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"runner": map[string]any{
				"id": "runner-1", "name": body.Name, "provider": body.Provider,
				"modelLabel": "", "createdAt": "2026-11-03T12:00:00Z",
			},
			"token": "ttag_issued",
		})
	})
	mux.HandleFunc("/v1/agent/runners/self/revoke", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer ttag_issued" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		calls.revokes.Add(1)
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/v1/agent/pair", func(w http.ResponseWriter, r *http.Request) {
		calls.pairs.Add(1)
		// The pairing code is the credential: no user JWT or runner token is sent.
		if auth := r.Header.Get("Authorization"); auth != "" {
			t.Errorf("pair Authorization = %q, want none", auth)
		}
		var body struct {
			Code       string `json:"code"`
			Name       string `json:"name"`
			Provider   string `json:"provider"`
			ModelLabel string `json:"modelLabel"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode pair: %v", err)
		}
		if body.Code != "ttpc_issued" {
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "pairing code is invalid or expired"})
			return
		}
		if body.Name != "box" || body.Provider != "echo" {
			t.Errorf("pair body = %+v, want the machine name and provider", body)
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"runner": map[string]any{
				"id": "runner-2", "name": body.Name, "provider": body.Provider,
				"modelLabel": body.ModelLabel, "host": "remote",
				"createdAt": "2026-11-03T12:00:00Z",
			},
			"token": "ttag_paired",
		})
	})
	return httptest.NewServer(mux)
}

func TestAgentConnectStatusAndDisconnect(t *testing.T) {
	var calls agentServerCalls
	server := agentServer(t, &calls)
	defer server.Close()

	configPath := filepath.Join(t.TempDir(), "agent.json")
	t.Setenv("TT_AGENT_CONFIG", configPath)
	statePath := filepath.Join(t.TempDir(), "install.json")

	runCLI := func(args []string, stdin string) (int, string, string) {
		var stdout, stderr bytes.Buffer
		code := run(context.Background(), args, strings.NewReader(stdin), &stdout, &stderr,
			statePath, "dev", install.ExecRunner{Stdout: &stdout, Stderr: &stderr})
		return code, stdout.String(), stderr.String()
	}

	code, out, errOut := runCLI([]string{
		"agent", "connect", "--server", server.URL + "/", "--email", "dev@example.com", "--name", "box",
	}, "secret\n")
	if code != 0 {
		t.Fatalf("connect = %d; stdout %s stderr %s", code, out, errOut)
	}
	if !strings.Contains(out, "connected box") || !strings.Contains(out, "next: tt agent run") {
		t.Errorf("connect output = %q, want the confirmation and next step", out)
	}

	cfg, err := runner.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.RunnerID != "runner-1" || cfg.Token != "ttag_issued" {
		t.Errorf("config = %+v, want the issued runner token", cfg)
	}
	if cfg.ServerURL != server.URL {
		t.Errorf("ServerURL = %q, want the trailing slash trimmed to %q", cfg.ServerURL, server.URL)
	}

	code, out, errOut = runCLI([]string{"agent", "status"}, "")
	if code != 0 {
		t.Fatalf("status = %d; stdout %s stderr %s", code, out, errOut)
	}
	if !strings.Contains(out, "runner-1") || !strings.Contains(out, "reachable: yes") {
		t.Errorf("status output = %q, want the runner and a reachability line", out)
	}
	if strings.Contains(out, "ttag_issued") {
		t.Error("status printed the stored token")
	}

	// A second pairing without --force must not overwrite a working credential.
	if code, _, stderr := runCLI([]string{"agent", "connect", "--server", server.URL, "--email", "dev@example.com"}, "secret\n"); code != 1 {
		t.Errorf("second connect = %d, want 1; stderr %s", code, stderr)
	}

	code, out, errOut = runCLI([]string{"agent", "disconnect"}, "")
	if code != 0 {
		t.Fatalf("disconnect = %d; stdout %s stderr %s", code, out, errOut)
	}
	if calls.revokes.Load() != 1 {
		t.Errorf("revoke requests = %d, want 1", calls.revokes.Load())
	}
	if calls.pairs.Load() != 0 {
		t.Errorf("pair requests = %d, want 0: the email path must not redeem a code", calls.pairs.Load())
	}
	if _, err := os.Stat(configPath); !os.IsNotExist(err) {
		t.Error("the config survived a disconnect")
	}

	if code, _, stderr := runCLI([]string{"agent", "status"}, ""); code != 1 || !strings.Contains(stderr, "not connected") {
		t.Errorf("status after disconnect = %d, %q; want 1 and not connected", code, stderr)
	}
}

func TestAgentConnectRefusesAnUnknownProvider(t *testing.T) {
	t.Setenv("TT_AGENT_CONFIG", filepath.Join(t.TempDir(), "agent.json"))

	var stdout, stderr bytes.Buffer
	code := run(context.Background(),
		[]string{"agent", "connect", "--server", "http://127.0.0.1:1", "--email", "dev@example.com", "--provider", "gpt"},
		strings.NewReader("secret\n"), &stdout, &stderr,
		filepath.Join(t.TempDir(), "install.json"), "dev", install.ExecRunner{Stdout: &stdout, Stderr: &stderr})

	if code != 2 {
		t.Fatalf("connect = %d, want 2; stderr %s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "unknown provider") || !strings.Contains(stderr.String(), "openai-compatible") {
		t.Errorf("error = %q, want the refusal and the supported list", stderr.String())
	}
}

func TestAgentConnectRefusesAMalformedServerURL(t *testing.T) {
	t.Setenv("TT_AGENT_CONFIG", filepath.Join(t.TempDir(), "agent.json"))

	var stdout, stderr bytes.Buffer
	code := run(context.Background(),
		[]string{"agent", "connect", "--server", "ftp://example.com", "--email", "dev@example.com"},
		strings.NewReader("secret\n"), &stdout, &stderr,
		filepath.Join(t.TempDir(), "install.json"), "dev", install.ExecRunner{Stdout: &stdout, Stderr: &stderr})

	if code != 2 {
		t.Fatalf("connect = %d, want 2; stdout %s stderr %s", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "http or https") {
		t.Errorf("error = %q, want it to name the accepted schemes", stderr.String())
	}
}

func runAgentCLI(t *testing.T, args []string, stdin string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), args, strings.NewReader(stdin), &stdout, &stderr,
		filepath.Join(t.TempDir(), "install.json"), "dev",
		install.ExecRunner{Stdout: &stdout, Stderr: &stderr})
	return code, stdout.String(), stderr.String()
}

// The whole point of --pairing-code is that a machine can pair without a member
// signing in on it. Empty stdin makes any email or password prompt fail with EOF,
// so a zero exit proves neither was asked for.
func TestAgentConnectRedeemsAPairingCodeWithoutAskingForCredentials(t *testing.T) {
	var calls agentServerCalls
	server := agentServer(t, &calls)
	defer server.Close()

	configPath := filepath.Join(t.TempDir(), "agent.json")
	t.Setenv("TT_AGENT_CONFIG", configPath)

	code, out, errOut := runAgentCLI(t, []string{
		"agent", "connect", "--server", server.URL + "/",
		"--pairing-code", "ttpc_issued", "--name", "box",
	}, "")
	if code != 0 {
		t.Fatalf("connect = %d; stdout %s stderr %s", code, out, errOut)
	}
	if !strings.Contains(out, "connected box") {
		t.Errorf("connect output = %q, want the paired machine name", out)
	}
	if strings.Contains(out, "Email") || strings.Contains(out, "Password") {
		t.Errorf("the pairing path prompted for credentials: %q", out)
	}

	cfg, err := runner.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.RunnerID != "runner-2" || cfg.Token != "ttag_paired" || cfg.Provider != "echo" {
		t.Errorf("config = %+v, want the runner and token returned by /v1/agent/pair", cfg)
	}
	if cfg.ServerURL != server.URL {
		t.Errorf("ServerURL = %q, want %q", cfg.ServerURL, server.URL)
	}
	if calls.pairs.Load() != 1 || calls.logins.Load() != 0 || calls.registrations.Load() != 0 {
		t.Errorf("calls = pairs %d, logins %d, registrations %d; want only one pair",
			calls.pairs.Load(), calls.logins.Load(), calls.registrations.Load())
	}
}

// The app shows `tt agent connect --pairing-code …` with no --server, so the
// copied command must work by asking for the server URL and nothing else.
func TestAgentConnectWithOnlyAPairingCodeAsksJustForTheServer(t *testing.T) {
	var calls agentServerCalls
	server := agentServer(t, &calls)
	defer server.Close()

	configPath := filepath.Join(t.TempDir(), "agent.json")
	t.Setenv("TT_AGENT_CONFIG", configPath)

	code, out, errOut := runAgentCLI(t, []string{
		"agent", "connect", "--pairing-code", "ttpc_issued", "--name", "box",
	}, server.URL+"\n")
	if code != 0 {
		t.Fatalf("connect = %d; stdout %s stderr %s", code, out, errOut)
	}
	if !strings.Contains(out, "Server URL") {
		t.Errorf("output = %q, want the server URL prompt", out)
	}
	if strings.Contains(out, "Email") || strings.Contains(out, "Password") {
		t.Errorf("the pairing path prompted for credentials: %q", out)
	}
	cfg, err := runner.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.ServerURL != server.URL || cfg.Token != "ttag_paired" {
		t.Errorf("config = %+v, want the prompted server and the paired token", cfg)
	}
}

// A spent, expired or invented code gets the server's one 401 sentence. The CLI
// must not write a config for it, or the machine looks paired until its first run.
func TestAgentConnectRefusesAnInvalidPairingCode(t *testing.T) {
	var calls agentServerCalls
	server := agentServer(t, &calls)
	defer server.Close()

	configPath := filepath.Join(t.TempDir(), "agent.json")
	t.Setenv("TT_AGENT_CONFIG", configPath)

	code, out, errOut := runAgentCLI(t, []string{
		"agent", "connect", "--server", server.URL,
		"--pairing-code", "ttpc_spent", "--name", "box",
	}, "")
	if code != 1 {
		t.Fatalf("connect = %d, want 1; stdout %s stderr %s", code, out, errOut)
	}
	if !strings.Contains(errOut, "pairing code is invalid or expired") {
		t.Errorf("error = %q, want the server's sentence", errOut)
	}
	if _, err := os.Stat(configPath); !os.IsNotExist(err) {
		t.Errorf("a refused pairing wrote a config: %v", err)
	}
	if calls.logins.Load() != 0 || calls.registrations.Load() != 0 {
		t.Errorf("a failed pair fell back to the email flow: logins %d, registrations %d",
			calls.logins.Load(), calls.registrations.Load())
	}
}

// An explicitly empty code must not quietly switch to the email flow: that would
// put a member's password prompt on a machine meant to pair with a code.
func TestAgentConnectRefusesAnEmptyPairingCode(t *testing.T) {
	t.Setenv("TT_AGENT_CONFIG", filepath.Join(t.TempDir(), "agent.json"))

	code, out, errOut := runAgentCLI(t, []string{
		"agent", "connect", "--server", "http://127.0.0.1:1", "--pairing-code", "",
	}, "")
	if code != 2 {
		t.Fatalf("connect = %d, want 2; stdout %s stderr %s", code, out, errOut)
	}
	if !strings.Contains(errOut, "needs a ttpc_") {
		t.Errorf("error = %q, want the blank-code refusal", errOut)
	}
	if strings.Contains(out, "Email") || strings.Contains(out, "Password") {
		t.Errorf("the blank code fell through to the email flow: %q", out)
	}
}

func TestAgentConnectRefusesPairingCodeWithEmail(t *testing.T) {
	t.Setenv("TT_AGENT_CONFIG", filepath.Join(t.TempDir(), "agent.json"))

	code, out, errOut := runAgentCLI(t, []string{
		"agent", "connect", "--server", "http://127.0.0.1:1",
		"--email", "dev@example.com", "--pairing-code", "ttpc_issued",
	}, "")
	if code != 2 {
		t.Fatalf("connect = %d, want 2; stdout %s stderr %s", code, out, errOut)
	}
	if !strings.Contains(errOut, "not both") {
		t.Errorf("error = %q, want the mutual-exclusion message", errOut)
	}
}
