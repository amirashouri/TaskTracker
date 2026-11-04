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

// agentServer is the three routes pairing needs, plus the health probe. It checks
// the credentials, because storing the wrong one is the failure that only shows up
// later, at the first run.
func agentServer(t *testing.T, revokes *atomic.Int32) *httptest.Server {
	t.Helper()

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("/v1/auth/login", func(w http.ResponseWriter, r *http.Request) {
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
		revokes.Add(1)
		w.WriteHeader(http.StatusNoContent)
	})
	return httptest.NewServer(mux)
}

func TestAgentConnectStatusAndDisconnect(t *testing.T) {
	var revokes atomic.Int32
	server := agentServer(t, &revokes)
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
	if revokes.Load() != 1 {
		t.Errorf("revoke requests = %d, want 1", revokes.Load())
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
