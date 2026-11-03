package agent

import (
	"context"
	"log/slog"
	"time"

	"github.com/amirashouri/tasktracker-server/internal/entities"
)

// LostRunnerDetail is the error a reaped run carries. It is a stable phrase
// rather than a sentence built per run, so the app can show it verbatim and a
// search for it finds every run a dead worker left behind.
const LostRunnerDetail = "runner lost"

// RunLeaseReaper fails runs whose worker stopped heartbeating, until ctx is done.
//
// A claim is a lease, not a lock: a runner that crashes, loses its network, or is
// killed leaves no notice behind, so the only way its run stops holding the work
// item's one live slot is for the server to notice the missing heartbeat. That is
// this loop.
//
// interval is how often to look, not how long a lease lasts. It is passed in so
// the composition root can tie it to the configured lease, and so a test can run
// the loop in milliseconds. The first pass happens after one interval, matching
// the change-feed pruner: a startup that reaps immediately would race a worker
// whose next heartbeat is already in flight.
func RunLeaseReaper(ctx context.Context, repo *entities.AgentRepository, interval time.Duration) {
	if interval <= 0 {
		interval = 30 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			reaped, err := repo.ReapExpiredRuns(ctx, entities.Now(), LostRunnerDetail)
			if err != nil {
				// A cancelled context is the shutdown path, not a failure to log.
				if ctx.Err() != nil {
					return
				}
				slog.Error("reap expired agent runs", "error", err)
				continue
			}
			if len(reaped) > 0 {
				slog.Info("failed agent runs whose runner stopped heartbeating", "runs", len(reaped))
			}
		}
	}
}
