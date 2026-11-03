package entities

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/amirashouri/tasktracker-server/internal/db"
)

// agentFixture is the smallest world a run needs: a member, a project they are
// on, and an item in progress. The repository does not enforce the dispatch
// preconditions — the handler does — so the fixture only has to be realistic.
type agentFixture struct {
	database *db.DB
	repo     *AgentRepository
	user     *User
	project  *Project
	item     *WorkItem
}

func newAgentFixture(t *testing.T) agentFixture {
	t.Helper()
	database := newTestDB(t)
	ctx := context.Background()

	user := mustCreateUser(t, NewUserRepository(database.DB), "dev@example.com")
	project := &Project{KeyPrefix: "TT", Name: "Demo", MemberIDs: []UserID{user.ID}}
	if err := NewProjectRepository(database.DB).Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}

	item := &WorkItem{
		ProjectID: project.ID,
		Key:       "TT-1",
		Title:     "Fix the thing",
		Status:    WorkItemStatusInProgress,
		Priority:  PriorityMedium,
	}
	if err := NewWorkItemRepository(database.DB).Create(ctx, item); err != nil {
		t.Fatalf("create work item: %v", err)
	}

	return agentFixture{database: database, repo: NewAgentRepository(database.DB), user: user, project: project, item: item}
}

func (f agentFixture) mustRegisterRunner(t *testing.T, user *User, name, hash string) *AgentRunner {
	t.Helper()
	runner := &AgentRunner{UserID: user.ID, Name: name, Provider: "echo", ModelLabel: "test"}
	if err := f.repo.RegisterRunner(context.Background(), runner, hash); err != nil {
		t.Fatalf("register runner %s: %v", name, err)
	}
	return runner
}

func (f agentFixture) mustQueueRun(t *testing.T, workItem WorkItemID, createdAt Timestamp) *AgentRun {
	t.Helper()
	run := &AgentRun{
		WorkItemID:   workItem,
		ProjectID:    f.project.ID,
		RequestedBy:  f.user.ID,
		TaskDocument: `{"key":"TT-1","title":"Fix the thing"}`,
		CreatedAt:    createdAt,
	}
	if err := f.repo.CreateRun(context.Background(), run); err != nil {
		t.Fatalf("create run: %v", err)
	}
	return run
}

func lease() Timestamp { return Now() + 60_000 }

// The one-live-run rule is the partial index, so a second dispatch is refused by
// the insert rather than by a read the two dispatchers could both pass. The same
// test proves a terminal run frees the slot, which is what makes "retry" work.
func TestCreateRunRefusesASecondLiveRun(t *testing.T) {
	f := newAgentFixture(t)
	ctx := context.Background()

	first := f.mustQueueRun(t, f.item.ID, 0)
	if first.ID == "" {
		t.Fatal("CreateRun did not mint an id")
	}
	if first.Status != AgentRunQueued {
		t.Errorf("status = %q, want %q", first.Status, AgentRunQueued)
	}

	err := f.repo.CreateRun(ctx, &AgentRun{
		WorkItemID: f.item.ID, ProjectID: f.project.ID, RequestedBy: f.user.ID, TaskDocument: `{}`,
	})
	if !errors.Is(err, ErrAgentRunActive) {
		t.Fatalf("second live run = %v, want ErrAgentRunActive", err)
	}

	runner := f.mustRegisterRunner(t, f.user, "dev-machine", "hash-first")
	if _, err := f.repo.ClaimRun(ctx, runner.ID, f.user.ID, lease()); err != nil {
		t.Fatalf("claim: %v", err)
	}
	if _, err := f.repo.FinishRun(ctx, first.ID, runner.ID, AgentRunSucceeded, "done", ""); err != nil {
		t.Fatalf("finish: %v", err)
	}

	if err := f.repo.CreateRun(ctx, &AgentRun{
		WorkItemID: f.item.ID, ProjectID: f.project.ID, RequestedBy: f.user.ID, TaskDocument: `{}`,
	}); err != nil {
		t.Fatalf("create after the first run finished = %v, want accepted", err)
	}
}

// A runner may only take work its own user asked for: a teammate's machine must
// not silently execute somebody else's task.
func TestClaimRunTakesOnlyTheRunnersOwnRuns(t *testing.T) {
	f := newAgentFixture(t)
	ctx := context.Background()

	run := f.mustQueueRun(t, f.item.ID, 0)

	other := mustCreateUser(t, NewUserRepository(f.database.DB), "other@example.com")
	otherRunner := f.mustRegisterRunner(t, other, "other-machine", "hash-other")

	claimed, err := f.repo.ClaimRun(ctx, otherRunner.ID, other.ID, lease())
	if err != nil {
		t.Fatalf("claim by the other user: %v", err)
	}
	if claimed != nil {
		t.Fatalf("another user's runner claimed %s; runs are scoped to their requester", claimed.ID)
	}

	runner := f.mustRegisterRunner(t, f.user, "dev-machine", "hash-own")
	claimed, err = f.repo.ClaimRun(ctx, runner.ID, f.user.ID, lease())
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if claimed == nil || claimed.ID != run.ID {
		t.Fatalf("claim = %v, want run %s", claimed, run.ID)
	}
	if claimed.Status != AgentRunClaimed {
		t.Errorf("status = %q, want %q", claimed.Status, AgentRunClaimed)
	}
	if claimed.RunnerID == nil || *claimed.RunnerID != runner.ID {
		t.Errorf("runnerId = %v, want %s", claimed.RunnerID, runner.ID)
	}
	if claimed.StartedAt == nil || claimed.LeaseExpiresAt == nil {
		t.Error("a claim must stamp startedAt and a lease")
	}

	if again, err := f.repo.ClaimRun(ctx, runner.ID, f.user.ID, lease()); err != nil || again != nil {
		t.Errorf("a second claim = %v, %v; want nothing queued", again, err)
	}
}

// Membership is re-checked at claim time: a member removed after dispatching must
// not have their machine run the task, and the run stays queued rather than being
// handed to somebody else.
func TestClaimRunRequiresProjectMembership(t *testing.T) {
	database := newTestDB(t)
	ctx := context.Background()

	user := mustCreateUser(t, NewUserRepository(database.DB), "outsider@example.com")
	project := &Project{KeyPrefix: "TT", Name: "No members"} // the user is not on it
	if err := NewProjectRepository(database.DB).Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}
	item := &WorkItem{ProjectID: project.ID, Key: "TT-1", Title: "Task", Status: WorkItemStatusInProgress, Priority: PriorityLow}
	if err := NewWorkItemRepository(database.DB).Create(ctx, item); err != nil {
		t.Fatalf("create item: %v", err)
	}

	repo := NewAgentRepository(database.DB)
	runner := &AgentRunner{UserID: user.ID, Name: "dev-machine"}
	if err := repo.RegisterRunner(ctx, runner, "hash-outsider"); err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := repo.CreateRun(ctx, &AgentRun{
		WorkItemID: item.ID, ProjectID: project.ID, RequestedBy: user.ID, TaskDocument: `{}`,
	}); err != nil {
		t.Fatalf("create run: %v", err)
	}

	claimed, err := repo.ClaimRun(ctx, runner.ID, user.ID, lease())
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if claimed != nil {
		t.Error("a non-member's run was claimed; project membership must gate the claim")
	}
}

// Two runners waking at the same instant must not receive the same run. The claim
// is one statement with the status predicate in the outer WHERE, which is what
// makes the loser match zero rows instead of updating the winner's row.
func TestClaimRunIsAtomicAcrossRunners(t *testing.T) {
	f := newAgentFixture(t)
	ctx := context.Background()

	const runs = 6
	workItems := NewWorkItemRepository(f.database.DB)
	for i := 0; i < runs; i++ {
		item := &WorkItem{
			ProjectID: f.project.ID,
			Key:       fmt.Sprintf("TT-%d", i+2),
			Title:     "Task",
			Status:    WorkItemStatusInProgress,
			Priority:  PriorityLow,
		}
		if err := workItems.Create(ctx, item); err != nil {
			t.Fatalf("create item %d: %v", i, err)
		}
		f.mustQueueRun(t, item.ID, Timestamp(1_700_000_000_000+int64(i)))
	}

	claimed := make([]*AgentRun, runs)
	errs := make([]error, runs)
	var wg sync.WaitGroup
	for i := 0; i < runs; i++ {
		runner := f.mustRegisterRunner(t, f.user, fmt.Sprintf("machine-%d", i), fmt.Sprintf("hash-%d", i))
		wg.Add(1)
		go func(i int, runnerID AgentRunnerID) {
			defer wg.Done()
			claimed[i], errs[i] = f.repo.ClaimRun(ctx, runnerID, f.user.ID, lease())
		}(i, runner.ID)
	}
	wg.Wait()

	seen := map[AgentRunID]bool{}
	for i, run := range claimed {
		if errs[i] != nil {
			t.Fatalf("claim %d: %v", i, errs[i])
		}
		if run == nil {
			t.Fatalf("claim %d returned nothing with %d runs queued", i, runs)
		}
		if seen[run.ID] {
			t.Errorf("run %s was claimed twice", run.ID)
		}
		seen[run.ID] = true
	}
	if len(seen) != runs {
		t.Errorf("claimed %d distinct runs, want %d", len(seen), runs)
	}
}

// The event batch is also a heartbeat: it promotes claimed → running and renews
// the lease, so a long tool call is not mistaken for a dead runner. The cancel
// flag is read on the beat, which is what makes cancel cooperative.
func TestAppendEventsAssignsSeqAndRenewsTheLease(t *testing.T) {
	f := newAgentFixture(t)
	ctx := context.Background()

	run := f.mustQueueRun(t, f.item.ID, 0)
	runner := f.mustRegisterRunner(t, f.user, "dev-machine", "hash-events")
	if _, err := f.repo.ClaimRun(ctx, runner.ID, f.user.ID, lease()); err != nil {
		t.Fatalf("claim: %v", err)
	}

	lastSeq, err := f.repo.AppendEvents(ctx, run.ID, runner.ID, lease(), []AgentRunEvent{
		{Kind: "status", Message: "reading TT-1"},
		{Kind: "log", Message: "opened 3 files", Payload: `{"files":3}`},
	})
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	if lastSeq != 2 {
		t.Errorf("lastSeq = %d, want 2", lastSeq)
	}

	lastSeq, err = f.repo.AppendEvents(ctx, run.ID, runner.ID, lease(), []AgentRunEvent{{Kind: "log", Message: "editing"}})
	if err != nil {
		t.Fatalf("append second batch: %v", err)
	}
	if lastSeq != 3 {
		t.Errorf("lastSeq = %d, want 3", lastSeq)
	}

	stored, err := f.repo.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if stored.Status != AgentRunRunning {
		t.Errorf("status = %q, want %q after the first batch", stored.Status, AgentRunRunning)
	}
	if stored.LeaseExpiresAt == nil {
		t.Error("an event batch must extend the lease")
	}

	if _, err := f.repo.RequestCancel(ctx, run.ID); err != nil {
		t.Fatalf("request cancel: %v", err)
	}
	cancelling, _, err := f.repo.HeartbeatRun(ctx, run.ID, runner.ID, lease())
	if err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
	if !cancelling {
		t.Error("heartbeat did not report the pending cancel")
	}

	events, err := f.repo.ListEventsAfter(ctx, run.ID, 1, 50)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	if len(events) != 2 || events[0].Seq != 2 || events[1].Seq != 3 {
		t.Fatalf("events after seq 1 = %+v, want seq 2 and 3", events)
	}
	if events[0].Payload != `{"files":3}` {
		t.Errorf("payload = %q, want it re-served verbatim", events[0].Payload)
	}
}

// A stale or foreign worker must not write into a run it does not hold.
func TestEventsAndFinishRequireTheClaimingRunner(t *testing.T) {
	f := newAgentFixture(t)
	ctx := context.Background()

	run := f.mustQueueRun(t, f.item.ID, 0)
	owner := f.mustRegisterRunner(t, f.user, "owner-machine", "hash-owner")
	stranger := f.mustRegisterRunner(t, f.user, "stranger-machine", "hash-stranger")
	if _, err := f.repo.ClaimRun(ctx, owner.ID, f.user.ID, lease()); err != nil {
		t.Fatalf("claim: %v", err)
	}

	if _, err := f.repo.AppendEvents(ctx, run.ID, stranger.ID, lease(), []AgentRunEvent{{Kind: "log"}}); !errors.Is(err, ErrAgentRunNotClaimed) {
		t.Errorf("append by another runner = %v, want ErrAgentRunNotClaimed", err)
	}
	if _, _, err := f.repo.HeartbeatRun(ctx, run.ID, stranger.ID, lease()); !errors.Is(err, ErrAgentRunNotClaimed) {
		t.Errorf("heartbeat by another runner = %v, want ErrAgentRunNotClaimed", err)
	}
	if _, err := f.repo.FinishRun(ctx, run.ID, stranger.ID, AgentRunSucceeded, "done", ""); !errors.Is(err, ErrAgentRunNotClaimed) {
		t.Errorf("finish by another runner = %v, want ErrAgentRunNotClaimed", err)
	}
}

// A worker whose finish response was lost retries the same call; that must
// succeed and return the stored run rather than error, because the work is done.
func TestFinishRunIsIdempotentAndFreesTheSlot(t *testing.T) {
	f := newAgentFixture(t)
	ctx := context.Background()

	run := f.mustQueueRun(t, f.item.ID, 0)
	runner := f.mustRegisterRunner(t, f.user, "dev-machine", "hash-finish")
	if _, err := f.repo.ClaimRun(ctx, runner.ID, f.user.ID, lease()); err != nil {
		t.Fatalf("claim: %v", err)
	}

	finished, err := f.repo.FinishRun(ctx, run.ID, runner.ID, AgentRunSucceeded, "TT-12 is implemented", "")
	if err != nil {
		t.Fatalf("finish: %v", err)
	}
	if finished.Status != AgentRunSucceeded || finished.Summary == "" || finished.FinishedAt == nil {
		t.Fatalf("finished run = %+v, want a terminal run with a summary and a date", finished)
	}
	if finished.LeaseExpiresAt != nil {
		t.Error("a finished run must not keep a lease")
	}

	replayed, err := f.repo.FinishRun(ctx, run.ID, runner.ID, AgentRunSucceeded, "TT-12 is implemented", "")
	if err != nil {
		t.Fatalf("retried finish = %v, want success", err)
	}
	if replayed.FinishedAt == nil || *replayed.FinishedAt != *finished.FinishedAt {
		t.Error("a retried finish restamped finishedAt instead of returning the stored run")
	}

	if _, err := f.repo.FinishRun(ctx, run.ID, runner.ID, AgentRunQueued, "", ""); err == nil {
		t.Error("finishing as a non-terminal status should be refused")
	}
}

// Revocation takes effect on the next lookup, is owner-scoped, and is idempotent
// without restamping the date.
func TestRunnerTokensAreOwnerScopedAndRevocable(t *testing.T) {
	database := newTestDB(t)
	ctx := context.Background()

	users := NewUserRepository(database.DB)
	owner := mustCreateUser(t, users, "owner@example.com")
	stranger := mustCreateUser(t, users, "stranger@example.com")
	repo := NewAgentRepository(database.DB)

	runner := &AgentRunner{UserID: owner.ID, Name: "dev-machine", Provider: "openai-compatible", ModelLabel: "llama3.1"}
	if err := repo.RegisterRunner(ctx, runner, "hash-registered"); err != nil {
		t.Fatalf("register: %v", err)
	}

	found, err := repo.RunnerByTokenHash(ctx, "hash-registered")
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if found == nil || found.ID != runner.ID {
		t.Fatalf("lookup = %v, want runner %s", found, runner.ID)
	}
	if found.Provider != "openai-compatible" || found.ModelLabel != "llama3.1" {
		t.Errorf("advertised provider/model = %q/%q, want them stored", found.Provider, found.ModelLabel)
	}

	if err := repo.RevokeRunner(ctx, runner.ID, stranger.ID); !errors.Is(err, ErrAgentRunnerNotFound) {
		t.Errorf("revoking another user's runner = %v, want ErrAgentRunnerNotFound", err)
	}
	if err := repo.RevokeRunner(ctx, runner.ID, owner.ID); err != nil {
		t.Fatalf("revoke: %v", err)
	}

	gone, err := repo.RunnerByTokenHash(ctx, "hash-registered")
	if err != nil {
		t.Fatalf("lookup after revoke: %v", err)
	}
	if gone != nil {
		t.Error("a revoked runner's token still resolves")
	}

	list, err := repo.ListRunners(ctx, owner.ID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 || list[0].RevokedAt == nil {
		t.Fatalf("ListRunners = %+v, want the revoked machine included", list)
	}
	firstRevokedAt := *list[0].RevokedAt

	if err := repo.RevokeRunner(ctx, runner.ID, owner.ID); err != nil {
		t.Fatalf("second revoke: %v", err)
	}
	again, err := repo.ListRunners(ctx, owner.ID)
	if err != nil {
		t.Fatalf("list after second revoke: %v", err)
	}
	if again[0].RevokedAt == nil || *again[0].RevokedAt != firstRevokedAt {
		t.Error("a repeated revoke restamped revokeAt")
	}

	unknown, err := repo.RunnerByTokenHash(ctx, "hash-never-registered")
	if err != nil {
		t.Fatalf("unknown token lookup: %v", err)
	}
	if unknown != nil {
		t.Error("an unregistered token resolved to a runner")
	}
}

func TestListRunsByWorkItemReturnsNewestFirst(t *testing.T) {
	f := newAgentFixture(t)
	ctx := context.Background()

	runner := f.mustRegisterRunner(t, f.user, "dev-machine", "hash-runs")
	created := []Timestamp{1_700_000_000_000, 1_700_000_100_000, 1_700_000_200_000}
	for _, at := range created {
		run := f.mustQueueRun(t, f.item.ID, at)
		if _, err := f.repo.ClaimRun(ctx, runner.ID, f.user.ID, lease()); err != nil {
			t.Fatalf("claim: %v", err)
		}
		if _, err := f.repo.FinishRun(ctx, run.ID, runner.ID, AgentRunFailed, "", "boom"); err != nil {
			t.Fatalf("finish: %v", err)
		}
	}

	runs, err := f.repo.ListRunsByWorkItem(ctx, f.item.ID, 20)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(runs) != 3 {
		t.Fatalf("listed %d runs, want 3", len(runs))
	}
	for i, want := range []Timestamp{created[2], created[1], created[0]} {
		if runs[i].CreatedAt != want {
			t.Errorf("run %d createdAt = %d, want %d", i, runs[i].CreatedAt, want)
		}
	}
}
