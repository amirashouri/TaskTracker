package runner

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestClientSendsTheRunnerContract(t *testing.T) {
	var mu sync.Mutex
	var authHeaders []string
	var finishBody map[string]string
	var eventCount int

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		authHeaders = append(authHeaders, r.Header.Get("Authorization"))
		mu.Unlock()

		switch r.URL.Path {
		case "/v1/agent/runs/claim":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"run": map[string]any{
					"id":           "run-1",
					"workItemId":   "w-1",
					"taskDocument": `{"item":{"key":"TT-1","title":"Fix the thing"}}`,
				},
			})
		case "/v1/agent/runs/run-1/events":
			var body struct {
				Events []Event `json:"events"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode events: %v", err)
			}
			mu.Lock()
			eventCount = len(body.Events)
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"accepted": len(body.Events), "nextSeq": 7})
		case "/v1/agent/runs/run-1/heartbeat":
			_ = json.NewEncoder(w).Encode(map[string]any{"cancelRequested": true})
		case "/v1/agent/runs/run-1/finish":
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode finish: %v", err)
			}
			mu.Lock()
			finishBody = body
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"run": map[string]any{"id": "run-1"}})
		case "/v1/agent/runners/self/revoke":
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	client := NewClient(server.URL, "ttag_test")
	ctx := context.Background()

	run, err := client.Claim(ctx, 1)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if run == nil || run.ID != "run-1" || run.WorkItemID != "w-1" {
		t.Fatalf("claim = %+v, want the returned run", run)
	}
	if !strings.Contains(run.TaskDocument, "TT-1") {
		t.Errorf("taskDocument = %q, want it carried verbatim", run.TaskDocument)
	}

	seq, err := client.PostEvents(ctx, run.ID, []Event{{Kind: "status", Message: "reading"}})
	if err != nil || seq != 7 {
		t.Errorf("PostEvents = %d, %v; want 7", seq, err)
	}

	cancelRequested, err := client.Heartbeat(ctx, run.ID)
	if err != nil || !cancelRequested {
		t.Errorf("Heartbeat = %v, %v; want true", cancelRequested, err)
	}

	if err := client.Finish(ctx, run.ID, "succeeded", "done", ""); err != nil {
		t.Fatalf("finish: %v", err)
	}
	if err := client.RevokeSelf(ctx); err != nil {
		t.Fatalf("revoke self: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if eventCount != 1 {
		t.Errorf("posted %d events, want 1", eventCount)
	}
	if finishBody["outcome"] != "succeeded" || finishBody["summary"] != "done" {
		t.Errorf("finish body = %v, want the outcome and summary", finishBody)
	}
	for _, header := range authHeaders {
		if header != "Bearer ttag_test" {
			t.Errorf("Authorization = %q, want the runner token", header)
		}
	}
}

func TestClaimReturnsNilWhenNothingIsQueued(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	run, err := NewClient(server.URL, "ttag_test").Claim(context.Background(), 0)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if run != nil {
		t.Errorf("claim = %+v, want nil for a 204", run)
	}
}

// Two failures a worker treats differently: a 409 is about this run, a 401 means
// the machine's credential is gone.
func TestClientSurfacesTheServerSentenceAndRecognisesRevocation(t *testing.T) {
	conflict := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "run is not claimed by this runner"})
	}))
	defer conflict.Close()

	_, err := NewClient(conflict.URL, "ttag_test").Heartbeat(context.Background(), "run-1")
	if err == nil || !strings.Contains(err.Error(), "not claimed") {
		t.Fatalf("error = %v, want the server's sentence", err)
	}
	if IsUnauthorized(err) {
		t.Error("a 409 was read as a revoked credential")
	}

	unauthorized := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "unauthenticated"})
	}))
	defer unauthorized.Close()

	if _, err := NewClient(unauthorized.URL, "ttag_test").Heartbeat(context.Background(), "run-1"); !IsUnauthorized(err) {
		t.Errorf("error = %v, want IsUnauthorized", err)
	}
}

func TestLoginAndRegisterReadThePairingResponse(t *testing.T) {
	var registerAuth string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/auth/login":
			var body struct {
				Email    string `json:"email"`
				Password string `json:"password"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode login: %v", err)
			}
			if body.Email != "dev@example.com" || body.Password != "secret" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "jwt"})
		case "/v1/agent/runners":
			registerAuth = r.Header.Get("Authorization")
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"runner": map[string]any{
					"id": "runner-1", "name": "box", "provider": "echo",
					"modelLabel": "llama3.1", "createdAt": "2026-11-03T12:00:00Z",
				},
				"token": "ttag_issued",
			})
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	ctx := context.Background()
	access, err := Login(ctx, server.URL, "dev@example.com", "secret")
	if err != nil || access != "jwt" {
		t.Fatalf("Login = %q, %v; want jwt", access, err)
	}
	registration, err := Register(ctx, server.URL, access, "box", "echo", "llama3.1")
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if registration.RunnerID != "runner-1" || registration.Token != "ttag_issued" || registration.Name != "box" {
		t.Errorf("registration = %+v, want the returned id, name and token", registration)
	}
	if registerAuth != "Bearer jwt" {
		t.Errorf("registration Authorization = %q, want the access token", registerAuth)
	}
}
