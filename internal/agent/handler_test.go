package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/amirashouri/tasktracker-server/internal/db"
	"github.com/amirashouri/tasktracker-server/internal/dbtest"
	"github.com/amirashouri/tasktracker-server/internal/entities"
)

// agentEnv is one paired machine, its owner, and an item in progress — the
// smallest state a runner request needs. No run is queued: a test that wants one
// queues it, so "nothing queued" stays testable.
type agentEnv struct {
	handler  *Handler
	app      *AppHandler
	database *db.DB
	repo     *entities.AgentRepository
	users    *entities.UserRepository
	members  *fakeMembership
	user     *entities.User
	runner   *entities.AgentRunner
	token    string
	project  *entities.Project
	item     *entities.WorkItem
}

func newAgentEnv(t *testing.T) agentEnv {
	t.Helper()
	database := dbtest.NewDatabase(t)
	ctx := context.Background()

	users := entities.NewUserRepository(database.DB)
	user := mustCreateUser(t, users, "alice@example.com")
	project := &entities.Project{KeyPrefix: "TT", Name: "Demo", MemberIDs: []entities.UserID{user.ID}}
	if err := entities.NewProjectRepository(database.DB).Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}
	item := &entities.WorkItem{
		ProjectID: project.ID,
		Key:       "TT-1",
		Title:     "Fix the thing",
		Status:    entities.WorkItemStatusInProgress,
		Priority:  entities.PriorityMedium,
	}
	if err := entities.NewWorkItemRepository(database.DB).Create(ctx, item); err != nil {
		t.Fatalf("create work item: %v", err)
	}

	repo := entities.NewAgentRepository(database.DB)
	handler := NewHandler(repo, time.Minute)
	// A fixed clock makes the lease value predictable; nothing here needs time to
	// pass, and the claim tests use waitSeconds 0.
	handler.now = func() time.Time { return time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC) }

	runner, token := registerRunner(t, repo, user, "alice-machine", "ttag_alice_token")

	members := &fakeMembership{allow: true}
	app := NewAppHandler(
		repo,
		entities.NewWorkItemRepository(database.DB),
		entities.NewProjectRepository(database.DB),
		entities.NewCommentRepository(database.DB),
		users,
		members,
	)

	return agentEnv{
		handler:  handler,
		app:      app,
		database: database,
		repo:     repo,
		users:    users,
		members:  members,
		user:     user,
		runner:   runner,
		token:    token,
		project:  project,
		item:     item,
	}
}

func mustCreateUser(t *testing.T, users *entities.UserRepository, email string) *entities.User {
	t.Helper()
	user := &entities.User{Email: email, PasswordHash: "secret"}
	if err := users.Create(context.Background(), user); err != nil {
		t.Fatalf("create user %s: %v", email, err)
	}
	return user
}

func registerRunner(t *testing.T, repo *entities.AgentRepository, user *entities.User, name, token string) (*entities.AgentRunner, string) {
	t.Helper()
	runner := &entities.AgentRunner{UserID: user.ID, Name: name}
	if err := repo.RegisterRunner(context.Background(), runner, HashRunnerToken(token)); err != nil {
		t.Fatalf("register runner: %v", err)
	}
	return runner, token
}

func (e agentEnv) queueRun(t *testing.T) *entities.AgentRun {
	t.Helper()
	run := &entities.AgentRun{
		WorkItemID:   e.item.ID,
		ProjectID:    e.project.ID,
		RequestedBy:  e.user.ID,
		TaskDocument: `{"key":"TT-1","title":"Fix the thing","details":"make it work"}`,
	}
	if err := e.repo.CreateRun(context.Background(), run); err != nil {
		t.Fatalf("create run: %v", err)
	}
	return run
}

// authed wires a handler the way the composition root does.
func (e agentEnv) authed(handler http.HandlerFunc) http.Handler {
	return e.handler.Authenticate(handler)
}

func (e agentEnv) do(t *testing.T, handler http.Handler, method, path, token, runID string, body any) *httptest.ResponseRecorder {
	t.Helper()

	var payload []byte
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("encode body: %v", err)
		}
		payload = encoded
	}

	req := httptest.NewRequest(method, path, bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if runID != "" {
		req.SetPathValue("id", runID)
	}

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

// A missing token, a malformed one, an unknown one and a revoked one are all the
// same 401: telling them apart would let a caller probe which tokens exist. A
// live token must pass through to the handler, which answers 204 with nothing
// queued.
func TestAuthenticateAcceptsOnlyALiveRunnerToken(t *testing.T) {
	e := newAgentEnv(t)
	ctx := context.Background()

	revoked, revokedToken := registerRunner(t, e.repo, e.user, "retired-machine", "ttag_revoked")
	if err := e.repo.RevokeRunner(ctx, revoked.ID, e.user.ID); err != nil {
		t.Fatalf("revoke: %v", err)
	}

	cases := []struct {
		name  string
		token string
	}{
		{"no header", ""},
		{"user jwt", "eyJhbGciOiJIUzI1NiJ9.payload.signature"},
		{"unknown runner token", "ttag_never_registered"},
		{"revoked runner token", revokedToken},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := e.do(t, e.authed(e.handler.Claim), http.MethodPost, "/v1/agent/runs/claim", tc.token, "", map[string]any{"waitSeconds": 0})
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401; body %s", rec.Code, rec.Body)
			}
			var body map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode error body: %v", err)
			}
			if body["error"] != "unauthenticated" {
				t.Errorf("error = %q, want unauthenticated", body["error"])
			}
		})
	}

	live := e.do(t, e.authed(e.handler.Claim), http.MethodPost, "/v1/agent/runs/claim", e.token, "", map[string]any{"waitSeconds": 0})
	if live.Code != http.StatusNoContent {
		t.Fatalf("a live token got %d with nothing queued, want 204; body %s", live.Code, live.Body)
	}
}

// The claim response is the worker's whole input: the run's identity, its task
// snapshot as an opaque JSON *string*, and the lease. The app's shape never
// carries the snapshot, so this is checked on the raw keys rather than through
// the struct.
func TestClaimReturnsTheQueuedRunAndItsTaskDocument(t *testing.T) {
	e := newAgentEnv(t)
	run := e.queueRun(t)

	rec := e.do(t, e.authed(e.handler.Claim), http.MethodPost, "/v1/agent/runs/claim", e.token, "", map[string]any{"waitSeconds": 0})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", rec.Code, rec.Body)
	}

	var body struct {
		Run map[string]any `json:"run"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode claim: %v", err)
	}
	if body.Run["id"] != string(run.ID) {
		t.Errorf("run.id = %v, want %s", body.Run["id"], run.ID)
	}
	if body.Run["status"] != string(entities.AgentRunClaimed) {
		t.Errorf("run.status = %v, want claimed", body.Run["status"])
	}
	document, ok := body.Run["taskDocument"].(string)
	if !ok {
		t.Fatalf("taskDocument = %#v, want an opaque JSON string", body.Run["taskDocument"])
	}
	if document != run.TaskDocument {
		t.Errorf("taskDocument = %q, want it re-served verbatim", document)
	}
	if _, ok := body.Run["leaseExpiresAt"].(string); !ok {
		t.Errorf("leaseExpiresAt = %#v, want an ISO-8601 string", body.Run["leaseExpiresAt"])
	}

	// last_seen is the settings screen's "when did this machine last talk to us".
	runners, err := e.repo.ListRunners(context.Background(), e.user.ID)
	if err != nil {
		t.Fatalf("list runners: %v", err)
	}
	if runners[0].LastSeenAt == nil {
		t.Error("a claim did not record last_seen_at")
	}
}

// Another user's machine must not take the run. The scoping is the repository's,
// but the route is where it would be forgotten.
func TestClaimDoesNotTakeAnotherUsersRun(t *testing.T) {
	e := newAgentEnv(t)
	e.queueRun(t)

	other := mustCreateUser(t, e.users, "bob@example.com")
	_, otherToken := registerRunner(t, e.repo, other, "bob-machine", "ttag_bob_token")

	rec := e.do(t, e.authed(e.handler.Claim), http.MethodPost, "/v1/agent/runs/claim", otherToken, "", map[string]any{"waitSeconds": 0})
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; another user's run must stay queued. body %s", rec.Code, rec.Body)
	}
}

func TestEventBatchValidation(t *testing.T) {
	e := newAgentEnv(t)
	run := e.queueRun(t)

	cases := []struct {
		name string
		body any
	}{
		{"unknown field", map[string]any{"events": []any{map[string]any{"kind": "log"}}, "extra": true}},
		{"no events", map[string]any{"events": []any{}}},
		{"missing kind", map[string]any{"events": []any{map[string]any{"message": "no kind"}}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := e.do(t, e.authed(e.handler.AppendEvents), http.MethodPost, "/v1/agent/runs/"+string(run.ID)+"/events", e.token, string(run.ID), tc.body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body %s", rec.Code, rec.Body)
			}
		})
	}

	// Validation happens before the repository, so a run that was never claimed
	// still answers 400 rather than 409 here.
	var body map[string]any
	rec := e.do(t, e.authed(e.handler.AppendEvents), http.MethodPost, "/v1/agent/runs/"+string(run.ID)+"/events", e.token, string(run.ID), map[string]any{"events": []any{}})
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if body["error"] != "no events to append" {
		t.Errorf("error = %q, want %q", body["error"], "no events to append")
	}
}

// The batch assigns seq, promotes claimed → running, and renews the lease; the
// heartbeat then reports a cancel request set by the app side.
func TestEventsAndHeartbeatReportProgressAndCancel(t *testing.T) {
	e := newAgentEnv(t)
	ctx := context.Background()
	run := e.queueRun(t)
	path := "/v1/agent/runs/" + string(run.ID)

	if rec := e.do(t, e.authed(e.handler.Claim), http.MethodPost, "/v1/agent/runs/claim", e.token, "", map[string]any{"waitSeconds": 0}); rec.Code != http.StatusOK {
		t.Fatalf("claim: status %d; body %s", rec.Code, rec.Body)
	}

	rec := e.do(t, e.authed(e.handler.AppendEvents), http.MethodPost, path+"/events", e.token, string(run.ID), map[string]any{
		"events": []any{
			map[string]any{"kind": "status", "message": "reading TT-1"},
			map[string]any{"kind": "log", "message": "opened 3 files", "payload": `{"files":3}`},
		},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("append: status %d; body %s", rec.Code, rec.Body)
	}
	var appended struct {
		Accepted int   `json:"accepted"`
		NextSeq  int64 `json:"nextSeq"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &appended); err != nil {
		t.Fatalf("decode append: %v", err)
	}
	if appended.Accepted != 2 || appended.NextSeq != 2 {
		t.Errorf("append = %+v, want accepted 2 and nextSeq 2", appended)
	}

	if rec := e.do(t, e.authed(e.handler.Heartbeat), http.MethodPost, path+"/heartbeat", e.token, string(run.ID), nil); rec.Code != http.StatusOK {
		t.Fatalf("heartbeat: status %d; body %s", rec.Code, rec.Body)
	} else {
		var beat map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &beat); err != nil {
			t.Fatalf("decode heartbeat: %v", err)
		}
		if beat["cancelRequested"] != false {
			t.Errorf("cancelRequested = %v, want false before a cancel", beat["cancelRequested"])
		}
	}

	if _, err := e.repo.RequestCancel(ctx, run.ID); err != nil {
		t.Fatalf("request cancel: %v", err)
	}
	rec = e.do(t, e.authed(e.handler.Heartbeat), http.MethodPost, path+"/heartbeat", e.token, string(run.ID), nil)
	var beat map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &beat); err != nil {
		t.Fatalf("decode heartbeat after cancel: %v", err)
	}
	if beat["cancelRequested"] != true {
		t.Errorf("cancelRequested = %v, want true after a cancel request", beat["cancelRequested"])
	}
}

// The poll is what makes the runner idle cheaply: it holds the request open until
// a dispatch arrives, and answers the run in the same response.
func TestClaimLongPollsUntilARunArrives(t *testing.T) {
	e := newAgentEnv(t)
	e.handler.now = time.Now
	e.handler.pollInterval = 5 * time.Millisecond

	go func() {
		time.Sleep(30 * time.Millisecond)
		_ = e.repo.CreateRun(context.Background(), &entities.AgentRun{
			WorkItemID:   e.item.ID,
			ProjectID:    e.project.ID,
			RequestedBy:  e.user.ID,
			TaskDocument: `{"key":"TT-1"}`,
		})
	}()

	start := time.Now()
	rec := e.do(t, e.authed(e.handler.Claim), http.MethodPost, "/v1/agent/runs/claim", e.token, "", map[string]any{"waitSeconds": 2})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", rec.Code, rec.Body)
	}
	if elapsed := time.Since(start); elapsed < 20*time.Millisecond {
		t.Errorf("claim answered in %v without waiting for the queued run", elapsed)
	}
}

// A machine retires itself with its own token, and only itself: the CLI holds no
// user JWT, so this is the one revocation it can perform.
func TestRevokeSelfRetiresOnlyTheCallingMachine(t *testing.T) {
	e := newAgentEnv(t)
	_, otherToken := registerRunner(t, e.repo, e.user, "second-machine", "ttag_second")

	rec := e.do(t, e.authed(e.handler.RevokeSelf), http.MethodPost, "/v1/agent/runners/self/revoke", e.token, "", nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("revoke self = %d, want 204; body %s", rec.Code, rec.Body)
	}

	retired := e.do(t, e.authed(e.handler.Claim), http.MethodPost, "/v1/agent/runs/claim", e.token, "", map[string]any{"waitSeconds": 0})
	if retired.Code != http.StatusUnauthorized {
		t.Errorf("the retired token got %d, want 401", retired.Code)
	}

	other := e.do(t, e.authed(e.handler.Claim), http.MethodPost, "/v1/agent/runs/claim", otherToken, "", map[string]any{"waitSeconds": 0})
	if other.Code != http.StatusNoContent {
		t.Errorf("the other machine got %d, want 204: self-revoke must not touch it", other.Code)
	}
}

// A finish is chosen from a closed vocabulary, and one worker cannot finish a run
// another worker holds.
func TestFinishChoosesTheStatusAndRefusesAForeignRunner(t *testing.T) {
	e := newAgentEnv(t)
	run := e.queueRun(t)
	path := "/v1/agent/runs/" + string(run.ID)

	if rec := e.do(t, e.authed(e.handler.Claim), http.MethodPost, "/v1/agent/runs/claim", e.token, "", map[string]any{"waitSeconds": 0}); rec.Code != http.StatusOK {
		t.Fatalf("claim: status %d; body %s", rec.Code, rec.Body)
	}

	other := mustCreateUser(t, e.users, "bob@example.com")
	_, otherToken := registerRunner(t, e.repo, other, "bob-machine", "ttag_bob_token")
	if rec := e.do(t, e.authed(e.handler.Finish), http.MethodPost, path+"/finish", otherToken, string(run.ID), map[string]any{"outcome": "succeeded"}); rec.Code != http.StatusConflict {
		t.Fatalf("a foreign finish = %d, want 409; body %s", rec.Code, rec.Body)
	}

	if rec := e.do(t, e.authed(e.handler.Finish), http.MethodPost, path+"/finish", e.token, string(run.ID), map[string]any{"outcome": "gave up"}); rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown outcome = %d, want 400; body %s", rec.Code, rec.Body)
	}

	rec := e.do(t, e.authed(e.handler.Finish), http.MethodPost, path+"/finish", e.token, string(run.ID), map[string]any{
		"outcome": "succeeded", "summary": "TT-1 is implemented",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("finish: status %d; body %s", rec.Code, rec.Body)
	}
	var finished struct {
		Run map[string]any `json:"run"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &finished); err != nil {
		t.Fatalf("decode finish: %v", err)
	}
	if finished.Run["status"] != string(entities.AgentRunSucceeded) {
		t.Errorf("status = %v, want succeeded", finished.Run["status"])
	}
	if _, ok := finished.Run["finishedAt"].(string); !ok {
		t.Errorf("finishedAt = %#v, want an ISO-8601 string", finished.Run["finishedAt"])
	}
	if _, ok := finished.Run["taskDocument"]; ok {
		t.Error("finish re-sent the task document; the worker already has it")
	}

	// The worker retries after a lost response: same answer, not a conflict.
	if rec := e.do(t, e.authed(e.handler.Finish), http.MethodPost, path+"/finish", e.token, string(run.ID), map[string]any{"outcome": "succeeded"}); rec.Code != http.StatusOK {
		t.Fatalf("retried finish = %d, want 200; body %s", rec.Code, rec.Body)
	}
}
