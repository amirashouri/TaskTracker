package agent

import (
	"context"
	"testing"
	"time"

	"github.com/amirashouri/tasktracker-server/internal/entities"
)

// The loop is the only thing that recovers a work item from a dead worker, so it
// has to both reap and stop when the process is asked to.
func TestRunLeaseReaperFailsAStaleRunAndStopsOnCancel(t *testing.T) {
	e := newAgentEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	run := e.queueRun(t)
	if _, err := e.repo.ClaimRun(ctx, e.runner.ID, e.user.ID, entities.Timestamp(1_000)); err != nil {
		t.Fatalf("claim with a lapsed lease: %v", err)
	}

	done := make(chan struct{})
	go func() {
		RunLeaseReaper(ctx, e.repo, time.Millisecond)
		close(done)
	}()

	deadline := time.Now().Add(2 * time.Second)
	for {
		stored, err := e.repo.GetRun(ctx, run.ID)
		if err != nil {
			t.Fatalf("get run: %v", err)
		}
		if stored.Status == entities.AgentRunFailed {
			if stored.Error != LostRunnerDetail {
				t.Errorf("error = %q, want %q", stored.Error, LostRunnerDetail)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("run is still %q; the reaper did not fail a lapsed lease", stored.Status)
		}
		time.Sleep(time.Millisecond)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("the reaper did not stop when its context was cancelled")
	}
}
