package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/amirashouri/tasktracker-server/internal/auth"
	"github.com/amirashouri/tasktracker-server/internal/entities"
)

// fakeMembership stands in for sync.Engine's IsMember. The app routes only ask
// one question, so the fake answers it and the test flips it to prove the 403.
type fakeMembership struct{ allow bool }

func (f *fakeMembership) IsMember(context.Context, string, string) (bool, error) {
	return f.allow, nil
}

// doAs sends an app request authenticated as user. The real chain uses
// middleware.Auth; here the context is set directly so the test does not need a
// JWT manager to exercise a handler.
func doAs(t *testing.T, handler http.HandlerFunc, user *entities.User, method, path, pathID string, body any) *httptest.ResponseRecorder {
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
	if user != nil {
		req = req.WithContext(auth.WithUser(req.Context(), string(user.ID), entities.RoleMember))
	}
	if pathID != "" {
		req.SetPathValue("id", pathID)
	}

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

// The snapshot is the worker's whole brief, so it must carry the task as it was
// at dispatch: the details, the project's tag names, and the discussion that
// explains the work.
func TestDispatchSnapshotsTheTaskForTheWorker(t *testing.T) {
	e := newAgentEnv(t)
	ctx := context.Background()

	tagID := entities.TagID("33333333-3333-3333-3333-333333333333")
	e.project.Tags = []entities.ProjectTag{{ID: tagID, Name: "Bug", Color: "red", Icon: "bug"}}
	if err := entities.NewProjectRepository(e.database.DB).Update(ctx, e.project); err != nil {
		t.Fatalf("update project: %v", err)
	}
	e.item.Details = "make it work"
	e.item.TagIDs = []entities.TagID{tagID}
	if err := entities.NewWorkItemRepository(e.database.DB).Update(ctx, e.item); err != nil {
		t.Fatalf("update item: %v", err)
	}
	if err := entities.NewCommentRepository(e.database.DB).Create(ctx, &entities.Comment{
		WorkItemID: e.item.ID,
		AuthorID:   e.user.ID,
		Body:       "please fix the failing test",
	}); err != nil {
		t.Fatalf("create comment: %v", err)
	}

	rec := doAs(t, e.app.Dispatch, e.user, http.MethodPost, "/v1/agent/runs", "", map[string]any{"workItemId": e.item.ID})
	if rec.Code != http.StatusCreated {
		t.Fatalf("dispatch: status %d; body %s", rec.Code, rec.Body)
	}

	var created struct {
		Run appRunResponse `json:"run"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if created.Run.Status != entities.AgentRunQueued {
		t.Errorf("status = %q, want queued", created.Run.Status)
	}

	stored, err := e.repo.GetRun(ctx, created.Run.ID)
	if err != nil || stored == nil {
		t.Fatalf("read stored run: %v", err)
	}
	var document taskDocument
	if err := json.Unmarshal([]byte(stored.TaskDocument), &document); err != nil {
		t.Fatalf("decode task document: %v", err)
	}
	if document.Item.Key != "TT-1" || document.Item.Title != "Fix the thing" {
		t.Errorf("item = %+v, want the work item's key and title", document.Item)
	}
	if document.Item.Details != "make it work" {
		t.Errorf("details = %q, want the snapshot to carry them", document.Item.Details)
	}
	if document.Project.KeyPrefix != "TT" || document.Project.Name != "Demo" {
		t.Errorf("project = %+v, want the project's key prefix and name", document.Project)
	}
	if len(document.Item.Tags) != 1 || document.Item.Tags[0] != "Bug" {
		t.Errorf("tags = %v, want the catalogue name Bug", document.Item.Tags)
	}
	if len(document.Comments) != 1 || document.Comments[0].Author != "alice" {
		t.Errorf("comments = %+v, want the thread with its author", document.Comments)
	}
}

func TestDispatchRefusesWhatTheAppMustNotStart(t *testing.T) {
	e := newAgentEnv(t)
	ctx := context.Background()

	backlog := &entities.WorkItem{
		ProjectID: e.project.ID,
		Key:       "TT-2",
		Title:     "Not started yet",
		Status:    entities.WorkItemStatusTodo,
		Priority:  entities.PriorityLow,
	}
	if err := entities.NewWorkItemRepository(e.database.DB).Create(ctx, backlog); err != nil {
		t.Fatalf("create backlog item: %v", err)
	}

	cases := []struct {
		name     string
		item     entities.WorkItemID
		allow    bool
		want     int
		wantWord string
	}{
		{"not in progress", backlog.ID, true, http.StatusConflict, "not in progress"},
		{"not a member", e.item.ID, false, http.StatusForbidden, "not a member"},
		{"missing item", "00000000-0000-0000-0000-000000000000", true, http.StatusNotFound, "not found"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e.members.allow = tc.allow
			rec := doAs(t, e.app.Dispatch, e.user, http.MethodPost, "/v1/agent/runs", "", map[string]any{"workItemId": tc.item})
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d; body %s", rec.Code, tc.want, rec.Body)
			}
			if !strings.Contains(rec.Body.String(), tc.wantWord) {
				t.Errorf("error %s does not mention %q", rec.Body, tc.wantWord)
			}
		})
	}

	if rec := doAs(t, e.app.Dispatch, e.user, http.MethodPost, "/v1/agent/runs", "", map[string]any{}); rec.Code != http.StatusBadRequest {
		t.Errorf("dispatch without workItemId = %d, want 400", rec.Code)
	}
	if rec := doAs(t, e.app.Dispatch, nil, http.MethodPost, "/v1/agent/runs", "", map[string]any{"workItemId": e.item.ID}); rec.Code != http.StatusUnauthorized {
		t.Errorf("dispatch without a user = %d, want 401", rec.Code)
	}
}

// The one-live-run rule is the database's, so the second dispatch must be refused
// even though this handler never looked.
func TestDispatchRefusesASecondLiveRun(t *testing.T) {
	e := newAgentEnv(t)

	first := doAs(t, e.app.Dispatch, e.user, http.MethodPost, "/v1/agent/runs", "", map[string]any{"workItemId": e.item.ID})
	if first.Code != http.StatusCreated {
		t.Fatalf("first dispatch: status %d; body %s", first.Code, first.Body)
	}
	second := doAs(t, e.app.Dispatch, e.user, http.MethodPost, "/v1/agent/runs", "", map[string]any{"workItemId": e.item.ID})
	if second.Code != http.StatusConflict {
		t.Fatalf("second dispatch = %d, want 409; body %s", second.Code, second.Body)
	}
	if !strings.Contains(second.Body.String(), "already active") {
		t.Errorf("error %s does not say a run is already active", second.Body)
	}
}

// The progress sheet is one round trip per poll: the run plus the events after the
// cursor, with hasMore stating whether the page is full.
func TestRunProgressPagesByAfterSeq(t *testing.T) {
	e := newAgentEnv(t)
	ctx := context.Background()

	rec := doAs(t, e.app.Dispatch, e.user, http.MethodPost, "/v1/agent/runs", "", map[string]any{"workItemId": e.item.ID})
	var created struct {
		Run appRunResponse `json:"run"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode dispatch: %v", err)
	}

	lease := entities.Timestamp(time.Now().Add(time.Minute).UnixMilli())
	if _, err := e.repo.ClaimRun(ctx, e.runner.ID, e.user.ID, lease); err != nil {
		t.Fatalf("claim: %v", err)
	}
	if _, err := e.repo.AppendEvents(ctx, created.Run.ID, e.runner.ID, lease, []entities.AgentRunEvent{
		{Kind: "status", Message: "reading TT-1"},
		{Kind: "log", Message: "opening files"},
	}); err != nil {
		t.Fatalf("append: %v", err)
	}

	list := doAs(t, e.app.ListRuns, e.user, http.MethodGet, "/v1/agent/runs?workItemId="+string(e.item.ID), "", nil)
	if list.Code != http.StatusOK {
		t.Fatalf("list: status %d; body %s", list.Code, list.Body)
	}
	var runs runsResponse
	if err := json.Unmarshal(list.Body.Bytes(), &runs); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(runs.Runs) != 1 || runs.Runs[0].ID != created.Run.ID {
		t.Fatalf("runs = %+v, want the one dispatch", runs.Runs)
	}

	detail := doAs(t, e.app.GetRun, e.user, http.MethodGet, "/v1/agent/runs/"+string(created.Run.ID)+"?afterSeq=1", string(created.Run.ID), nil)
	if detail.Code != http.StatusOK {
		t.Fatalf("detail: status %d; body %s", detail.Code, detail.Body)
	}
	var page runDetailResponse
	if err := json.Unmarshal(detail.Body.Bytes(), &page); err != nil {
		t.Fatalf("decode detail: %v", err)
	}
	if len(page.Events) != 1 || page.Events[0].Seq != 2 || page.Events[0].Message != "opening files" {
		t.Fatalf("events after seq 1 = %+v, want just seq 2", page.Events)
	}
	if page.HasMore {
		t.Error("hasMore is true for a page that is not full")
	}

	bad := doAs(t, e.app.GetRun, e.user, http.MethodGet, "/v1/agent/runs/"+string(created.Run.ID)+"?afterSeq=-1", string(created.Run.ID), nil)
	if bad.Code != http.StatusBadRequest {
		t.Errorf("negative afterSeq = %d, want 400", bad.Code)
	}
}

// Cancel is a request the worker observes on its next heartbeat, so the app is
// told "requested" — and only a member may ask.
func TestCancelRequestsAndIsMemberScoped(t *testing.T) {
	e := newAgentEnv(t)

	rec := doAs(t, e.app.Dispatch, e.user, http.MethodPost, "/v1/agent/runs", "", map[string]any{"workItemId": e.item.ID})
	var created struct {
		Run appRunResponse `json:"run"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode: %v", err)
	}

	e.members.allow = false
	refused := doAs(t, e.app.Cancel, e.user, http.MethodPost, "/v1/agent/runs/"+string(created.Run.ID)+"/cancel", string(created.Run.ID), nil)
	if refused.Code != http.StatusForbidden {
		t.Fatalf("cancel by a non-member = %d, want 403", refused.Code)
	}

	e.members.allow = true
	cancelled := doAs(t, e.app.Cancel, e.user, http.MethodPost, "/v1/agent/runs/"+string(created.Run.ID)+"/cancel", string(created.Run.ID), nil)
	if cancelled.Code != http.StatusOK {
		t.Fatalf("cancel: status %d; body %s", cancelled.Code, cancelled.Body)
	}
	var body struct {
		Run appRunResponse `json:"run"`
	}
	if err := json.Unmarshal(cancelled.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode cancel: %v", err)
	}
	if !body.Run.CancelRequested {
		t.Error("cancelRequested is false after a cancel request")
	}
}

// Registering a machine returns the token once, that token opens the runner routes
// and nothing else, and revoking it closes the door on the next request.
func TestRunnerRegistrationReturnsAOneTimeUsableToken(t *testing.T) {
	e := newAgentEnv(t)

	rec := doAs(t, e.app.RegisterRunner, e.user, http.MethodPost, "/v1/agent/runners", "", map[string]any{
		"name": "laptop", "provider": "openai-compatible", "modelLabel": "llama3.1",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("register: status %d; body %s", rec.Code, rec.Body)
	}
	var registered registerRunnerResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &registered); err != nil {
		t.Fatalf("decode register: %v", err)
	}
	if registered.Runner.Name != "laptop" || registered.Runner.Provider != "openai-compatible" {
		t.Errorf("runner = %+v, want the advertised name and provider", registered.Runner)
	}
	if !strings.HasPrefix(registered.Token, "ttag_") || len(registered.Token) < 69 {
		t.Fatalf("token = %q, want a ttag_ token with 32 bytes of entropy", registered.Token)
	}

	claim := e.do(t, e.authed(e.handler.Claim), http.MethodPost, "/v1/agent/runs/claim", registered.Token, "", map[string]any{"waitSeconds": 0})
	if claim.Code != http.StatusNoContent {
		t.Fatalf("the new token got %d on a runner route, want 204; body %s", claim.Code, claim.Body)
	}

	list := doAs(t, e.app.ListRunners, e.user, http.MethodGet, "/v1/agent/runners", "", nil)
	var runners runnersResponse
	if err := json.Unmarshal(list.Body.Bytes(), &runners); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(runners.Runners) != 2 { // alice's fixture machine plus the new one
		t.Fatalf("runners = %d, want 2", len(runners.Runners))
	}

	revoked := doAs(t, e.app.RevokeRunner, e.user, http.MethodDelete, "/v1/agent/runners/"+string(registered.Runner.ID), string(registered.Runner.ID), nil)
	if revoked.Code != http.StatusNoContent {
		t.Fatalf("revoke: status %d; body %s", revoked.Code, revoked.Body)
	}
	if again := e.do(t, e.authed(e.handler.Claim), http.MethodPost, "/v1/agent/runs/claim", registered.Token, "", map[string]any{"waitSeconds": 0}); again.Code != http.StatusUnauthorized {
		t.Errorf("a revoked token got %d, want 401", again.Code)
	}
	// Revocation is idempotent on purpose: the row keeps its first date, so a
	// repeated revoke is a no-op rather than an error.
	if again := doAs(t, e.app.RevokeRunner, e.user, http.MethodDelete, "/v1/agent/runners/"+string(registered.Runner.ID), string(registered.Runner.ID), nil); again.Code != http.StatusNoContent {
		t.Errorf("revoking twice = %d, want 204", again.Code)
	}
	if unknown := doAs(t, e.app.RevokeRunner, e.user, http.MethodDelete, "/v1/agent/runners/00000000-0000-0000-0000-000000000000", "00000000-0000-0000-0000-000000000000", nil); unknown.Code != http.StatusNotFound {
		t.Errorf("revoking an unknown runner = %d, want 404", unknown.Code)
	}

	if bad := doAs(t, e.app.RegisterRunner, e.user, http.MethodPost, "/v1/agent/runners", "", map[string]any{"name": "  "}); bad.Code != http.StatusBadRequest {
		t.Errorf("registering without a name = %d, want 400", bad.Code)
	}
}

// Stop and start are the settings screen's two commands. The route is what
// requires an admin; this exercises the handler's own contract - the state
// changes and comes back on the runner the client renders.
func TestStopAndStartPauseARunner(t *testing.T) {
	e := newAgentEnv(t)

	stop := doAs(t, e.app.StopRunner, e.user, http.MethodPost,
		"/v1/agent/runners/"+string(e.runner.ID)+"/stop", string(e.runner.ID), nil)
	if stop.Code != http.StatusOK {
		t.Fatalf("stop: status %d; body %s", stop.Code, stop.Body)
	}
	var stopped runnerResponse
	if err := json.Unmarshal(stop.Body.Bytes(), &stopped); err != nil {
		t.Fatalf("decode stop: %v", err)
	}
	if stopped.Runner.PausedAt == nil {
		t.Error("the stop response carries no pausedAt; the screen cannot show it as stopped")
	}
	if stopped.Runner.RevokedAt != nil {
		t.Error("a stop revoked the machine; stop must be reversible")
	}

	start := doAs(t, e.app.StartRunner, e.user, http.MethodPost,
		"/v1/agent/runners/"+string(e.runner.ID)+"/start", string(e.runner.ID), nil)
	if start.Code != http.StatusOK {
		t.Fatalf("start: status %d; body %s", start.Code, start.Body)
	}
	var started runnerResponse
	if err := json.Unmarshal(start.Body.Bytes(), &started); err != nil {
		t.Fatalf("decode start: %v", err)
	}
	if started.Runner.PausedAt != nil {
		t.Error("pausedAt is still set after a start")
	}

	unknown := doAs(t, e.app.StopRunner, e.user, http.MethodPost,
		"/v1/agent/runners/00000000-0000-0000-0000-000000000000/stop",
		"00000000-0000-0000-0000-000000000000", nil)
	if unknown.Code != http.StatusNotFound {
		t.Errorf("stopping an unknown runner = %d, want 404", unknown.Code)
	}
	missing := doAs(t, e.app.StartRunner, e.user, http.MethodPost, "/v1/agent/runners//start", "", nil)
	if missing.Code != http.StatusBadRequest {
		t.Errorf("starting without an id = %d, want 400", missing.Code)
	}
}
