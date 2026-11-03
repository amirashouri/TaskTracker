package runner

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// blockingProvider waits for the worker to cancel it, which is what a real tool
// call does when the user cancels: the context is the signal.
type blockingProvider struct{}

func (blockingProvider) Name() string { return "blocking" }

func (blockingProvider) Execute(ctx context.Context, _ Run, _ func(Event)) Result {
	<-ctx.Done()
	return Result{Outcome: "cancelled", Summary: "the run was cancelled"}
}

// workerRecorder is a fake server that holds exactly one queued run.
type workerRecorder struct {
	t               *testing.T
	mu              sync.Mutex
	claimed         bool
	events          int
	outcome         string
	detail          string
	summary         string
	cancelHeartbeat bool
}

func (r *workerRecorder) handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/v1/agent/runs/claim", func(w http.ResponseWriter, _ *http.Request) {
		r.mu.Lock()
		already := r.claimed
		r.claimed = true
		r.mu.Unlock()
		if already {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"run": map[string]any{
				"id":           "run-1",
				"workItemId":   "w-1",
				"taskDocument": `{"item":{"key":"TT-1","title":"Fix the thing"}}`,
			},
		})
	})

	mux.HandleFunc("/v1/agent/runs/run-1/events", func(w http.ResponseWriter, req *http.Request) {
		var body struct {
			Events []Event `json:"events"`
		}
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			r.t.Errorf("decode events: %v", err)
		}
		r.mu.Lock()
		r.events += len(body.Events)
		total := r.events
		r.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"accepted": len(body.Events), "nextSeq": total})
	})

	mux.HandleFunc("/v1/agent/runs/run-1/heartbeat", func(w http.ResponseWriter, _ *http.Request) {
		r.mu.Lock()
		cancel := r.cancelHeartbeat
		r.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"cancelRequested": cancel})
	})

	mux.HandleFunc("/v1/agent/runs/run-1/finish", func(w http.ResponseWriter, req *http.Request) {
		var body struct {
			Outcome string `json:"outcome"`
			Summary string `json:"summary"`
			Error   string `json:"error"`
		}
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			r.t.Errorf("decode finish: %v", err)
		}
		r.mu.Lock()
		r.outcome, r.summary, r.detail = body.Outcome, body.Summary, body.Error
		r.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"run": map[string]any{"id": "run-1"}})
	})

	return mux
}

func (r *workerRecorder) snapshot() (events int, outcome, summary string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.events, r.outcome, r.summary
}

func TestWorkerClaimsReportsAndFinishes(t *testing.T) {
	recorder := &workerRecorder{t: t}
	server := httptest.NewServer(recorder.handler())
	defer server.Close()

	worker := NewWorker(NewClient(server.URL, "ttag_test"), EchoProvider{})
	worker.HeartbeatInterval = 30 * time.Millisecond

	if err := worker.Run(context.Background(), true); err != nil {
		t.Fatalf("Run: %v", err)
	}

	events, outcome, summary := recorder.snapshot()
	if outcome != "succeeded" {
		t.Errorf("outcome = %q, want succeeded", outcome)
	}
	if summary == "" {
		t.Error("finish carried no summary")
	}
	// The echo provider emits status, log and result.
	if events < 3 {
		t.Errorf("posted %d events, want at least 3", events)
	}
}

// A cancel arrives on the heartbeat, cancels the provider's context, and the run
// still reaches a terminal state — the slot must not be left to the reaper.
func TestWorkerReportsACancelledRun(t *testing.T) {
	recorder := &workerRecorder{t: t, cancelHeartbeat: true}
	server := httptest.NewServer(recorder.handler())
	defer server.Close()

	worker := NewWorker(NewClient(server.URL, "ttag_test"), blockingProvider{})
	worker.HeartbeatInterval = 5 * time.Millisecond

	if err := worker.Run(context.Background(), true); err != nil {
		t.Fatalf("Run: %v", err)
	}

	events, outcome, _ := recorder.snapshot()
	if outcome != "cancelled" {
		t.Errorf("outcome = %q, want cancelled", outcome)
	}
	if events != 0 {
		t.Errorf("a cancelled run posted %d events, want none", events)
	}
}
