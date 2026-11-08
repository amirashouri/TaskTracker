package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const (
	defaultWaitSeconds = 25
	defaultHeartbeat   = 20 * time.Second
	defaultRetryDelay  = 5 * time.Second
	defaultBatchSize   = 20
)

// Provider is one way of doing the work. `echo` exists so the claim → event →
// finish loop can be exercised without a model; the two model adapters live in
// openai.go and anthropic.go and share the tool loop in loop.go.
type Provider interface {
	Name() string
	Execute(ctx context.Context, run Run, emit func(Event)) Result
}

// Result is how a provider ends a run. Outcome is one of the server's three
// terminal words.
type Result struct {
	Outcome string
	Summary string
	Detail  string
	// Diff is the workspace's git diff after a successful run, when the
	// workspace is a git checkout. Empty otherwise.
	Diff string
}

// EchoProvider reports the task it was given and finishes. It is deterministic,
// which is what makes the worker loop testable end to end.
type EchoProvider struct{}

func (EchoProvider) Name() string { return "echo" }

func (EchoProvider) Execute(ctx context.Context, run Run, emit func(Event)) Result {
	emit(Event{Kind: "status", Message: "echo: received the task snapshot"})

	select {
	case <-ctx.Done():
		return Result{Outcome: "cancelled", Summary: "cancelled before echoing the task"}
	default:
	}

	emit(Event{
		Kind:    "log",
		Message: "echo: " + summarise(run.TaskDocument),
		Payload: `{"provider":"echo"}`,
	})
	emit(Event{
		Kind:    "result",
		Message: "echo: run complete",
		Payload: `{"provider":"echo"}`,
	})

	return Result{Outcome: "succeeded", Summary: "Echo worker finished the run"}
}

// Worker claims runs and reports on them until its context is cancelled.
type Worker struct {
	Client   *Client
	Provider Provider

	// These four are overridable so a test does not have to run in real seconds.
	WaitSeconds       int
	HeartbeatInterval time.Duration
	RetryDelay        time.Duration
	BatchSize         int

	// Log receives progress lines. A nil Log discards them.
	Log func(format string, args ...any)
}

func NewWorker(client *Client, provider Provider) *Worker {
	return &Worker{
		Client:            client,
		Provider:          provider,
		WaitSeconds:       defaultWaitSeconds,
		HeartbeatInterval: defaultHeartbeat,
		RetryDelay:        defaultRetryDelay,
		BatchSize:         defaultBatchSize,
	}
}

// Run claims and executes one run at a time. With once it returns after a single
// run, or immediately when nothing is queued.
//
// A claim failure is retried rather than fatal: a worker that exits on a blip is
// a worker somebody has to babysit, and the queue is durable on the server.
func (w *Worker) Run(ctx context.Context, once bool) error {
	for {
		if ctx.Err() != nil {
			return nil
		}

		run, err := w.Client.Claim(ctx, w.waitSeconds())
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			w.logf("claim failed: %v", err)
			if !sleep(ctx, w.retryDelay()) {
				return nil
			}
			continue
		}
		if run == nil {
			if once {
				return nil
			}
			continue
		}

		w.execute(ctx, run)
		if once {
			return nil
		}
	}
}

// execute reports progress, heartbeats, and finishes exactly once.
//
// The heartbeat is what makes cancel cooperative and what keeps the lease alive
// across a long tool call; the event batches renew it too, server-side.
func (w *Worker) execute(ctx context.Context, run *Run) {
	runCtx, cancel := context.WithCancel(ctx)
	heartbeatDone := make(chan struct{})

	go func() {
		defer close(heartbeatDone)
		ticker := time.NewTicker(w.heartbeat())
		defer ticker.Stop()
		for {
			select {
			case <-runCtx.Done():
				return
			case <-ticker.C:
				cancelRequested, err := w.Client.Heartbeat(runCtx, run.ID)
				if err != nil {
					if runCtx.Err() == nil {
						w.logf("heartbeat %s: %v", run.ID, err)
					}
					continue
				}
				if cancelRequested {
					w.logf("cancel requested for run %s", run.ID)
					cancel()
				}
			}
		}
	}()

	batch := make([]Event, 0, w.batchSize())
	emit := func(event Event) {
		batch = append(batch, event)
		if len(batch) < w.batchSize() {
			return
		}
		if _, err := w.Client.PostEvents(runCtx, run.ID, batch); err != nil {
			w.logf("progress for %s: %v", run.ID, err)
		}
		batch = batch[:0]
	}

	result := w.Provider.Execute(runCtx, *run, emit)
	cancelled := runCtx.Err() != nil
	cancel()
	<-heartbeatDone

	if cancelled && result.Outcome != "cancelled" {
		result = Result{Outcome: "cancelled", Summary: "the run was cancelled"}
	}

	// A cancelled context must not stop the last report: the run still has to
	// reach a terminal state, or its work item keeps the live slot forever.
	reportCtx, stop := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer stop()

	if len(batch) > 0 && !cancelled {
		if _, err := w.Client.PostEvents(reportCtx, run.ID, batch); err != nil {
			w.logf("progress for %s: %v", run.ID, err)
		}
	}

	outcome := result.Outcome
	switch outcome {
	case "succeeded", "failed", "cancelled":
	default:
		if result.Detail == "" {
			result.Detail = fmt.Sprintf("provider %q returned outcome %q", w.Provider.Name(), result.Outcome)
		}
		outcome = "failed"
	}

	if err := w.Client.Finish(reportCtx, run.ID, outcome, result.Summary, result.Detail, result.Diff); err != nil {
		w.logf("finish %s: %v", run.ID, err)
		return
	}
	w.logf("run %s finished %s", run.ID, outcome)
}

func (w *Worker) waitSeconds() int {
	if w.WaitSeconds <= 0 {
		return defaultWaitSeconds
	}
	return w.WaitSeconds
}

func (w *Worker) heartbeat() time.Duration {
	if w.HeartbeatInterval <= 0 {
		return defaultHeartbeat
	}
	return w.HeartbeatInterval
}

func (w *Worker) retryDelay() time.Duration {
	if w.RetryDelay <= 0 {
		return defaultRetryDelay
	}
	return w.RetryDelay
}

func (w *Worker) batchSize() int {
	if w.BatchSize <= 0 {
		return defaultBatchSize
	}
	return w.BatchSize
}

func (w *Worker) logf(format string, args ...any) {
	if w.Log != nil {
		w.Log(format, args...)
	}
}

// summarise shortens the task document for a progress line. The document is JSON
// and can be long; a log line is read at a glance, not parsed.
func summarise(document string) string {
	trimmed := strings.TrimSpace(document)
	if trimmed == "" {
		return "an empty task snapshot"
	}

	// Prefer the title, which is the part a person recognises.
	var task struct {
		Item struct {
			Key   string `json:"key"`
			Title string `json:"title"`
		} `json:"item"`
	}
	if err := json.Unmarshal([]byte(trimmed), &task); err == nil && task.Item.Title != "" {
		return fmt.Sprintf("%s — %s", task.Item.Key, task.Item.Title)
	}
	if len(trimmed) > 120 {
		return trimmed[:120] + "…"
	}
	return trimmed
}

// sleep waits for d, or returns false as soon as ctx is cancelled.
func sleep(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
