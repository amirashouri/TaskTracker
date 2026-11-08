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
	e.project.RepoURL = "https://github.com/acme/demo.git"
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
	var document contextGraph
	if err := json.Unmarshal([]byte(stored.TaskDocument), &document); err != nil {
		t.Fatalf("decode context graph: %v", err)
	}
	if document.SchemaVersion != contextGraphSchemaVersion {
		t.Errorf("schemaVersion = %d, want %d", document.SchemaVersion, contextGraphSchemaVersion)
	}
	if document.RunID != string(created.Run.ID) {
		t.Errorf("runId = %q, want %s", document.RunID, created.Run.ID)
	}
	if document.Task.Key != "TT-1" || document.Task.Title != "Fix the thing" {
		t.Errorf("task = %+v, want the work item's key and title", document.Task)
	}
	if document.Task.Details != "make it work" {
		t.Errorf("details = %q, want the brief to carry them", document.Task.Details)
	}
	if document.Project.KeyPrefix != "TT" || document.Project.Name != "Demo" {
		t.Errorf("project = %+v, want the project's key prefix and name", document.Project)
	}
	if document.Project.RepoURL != "https://github.com/acme/demo.git" {
		t.Errorf("repoUrl = %q, want the project's repository in the agent brief", document.Project.RepoURL)
	}
	if len(document.Task.Tags) != 1 || document.Task.Tags[0] != "Bug" {
		t.Errorf("tags = %v, want the catalogue name Bug", document.Task.Tags)
	}

	var comments []graphNode
	for _, node := range document.Nodes {
		if node.Type == nodeComment {
			comments = append(comments, node)
		}
	}
	if len(comments) != 1 || comments[0].Title != "alice" {
		t.Errorf("comment nodes = %+v, want the thread with its author", comments)
	}
	taskID := taskNodeID(e.item.ID)
	if !hasEdge(document.Edges, taskID, projectNodeID(e.project.ID), edgeBelongsTo) {
		t.Errorf("edges = %+v, want task -> project BELONGS_TO", document.Edges)
	}
	if !hasEdge(document.Edges, taskID, tagNodeID(tagID), edgeTaggedWith) {
		t.Errorf("edges = %+v, want task -> tag TAGGED_WITH", document.Edges)
	}
	if !hasEdge(document.Edges, taskID, comments[0].ID, edgeHasComment) {
		t.Errorf("edges = %+v, want task -> comment HAS_COMMENT", document.Edges)
	}
	if document.Limits.MaxNodes != maxContextNodes || document.Limits.MaxComments != maxContextComments {
		t.Errorf("limits = %+v, want the builder's bounds stated in the payload", document.Limits)
	}
}

func hasEdge(edges []graphEdge, from, to, kind string) bool {
	for _, edge := range edges {
		if edge.From == from && edge.To == to && edge.Type == kind {
			return true
		}
	}
	return false
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

// A retried dispatch with the same key returns the run the first request
// created. A different key is a new dispatch, and the one-live-run rule still
// refuses it while the first is queued.
func TestDispatchIsIdempotentByKey(t *testing.T) {
	e := newAgentEnv(t)
	ctx := context.Background()
	body := map[string]any{"workItemId": e.item.ID, "idempotencyKey": "dispatch-1"}

	first := doAs(t, e.app.Dispatch, e.user, http.MethodPost, "/v1/agent/runs", "", body)
	if first.Code != http.StatusCreated {
		t.Fatalf("first dispatch = %d, want 201; body %s", first.Code, first.Body)
	}
	var created runResponse
	if err := json.Unmarshal(first.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode first dispatch: %v", err)
	}

	second := doAs(t, e.app.Dispatch, e.user, http.MethodPost, "/v1/agent/runs", "", body)
	if second.Code != http.StatusOK {
		t.Fatalf("replayed dispatch = %d, want 200; body %s", second.Code, second.Body)
	}
	var replay runResponse
	if err := json.Unmarshal(second.Body.Bytes(), &replay); err != nil {
		t.Fatalf("decode replay: %v", err)
	}
	if replay.Run.ID != created.Run.ID {
		t.Errorf("replay returned run %s, want the stored %s", replay.Run.ID, created.Run.ID)
	}

	runs, err := e.repo.ListRunsByWorkItem(ctx, e.item.ID, 10)
	if err != nil {
		t.Fatalf("list runs: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("stored %d runs, want 1: an idempotent replay created a duplicate", len(runs))
	}

	other := doAs(t, e.app.Dispatch, e.user, http.MethodPost, "/v1/agent/runs", "", map[string]any{
		"workItemId": e.item.ID, "idempotencyKey": "dispatch-2",
	})
	if other.Code != http.StatusConflict {
		t.Errorf("a different key while a run is live = %d, want 409; body %s", other.Code, other.Body)
	}
}

// A pairing code is spent once, expires, and binds the machine to the code's
// project. The machine's own request cannot name a different project or owner.
func TestPairingCodeIsSingleUseAndProjectScoped(t *testing.T) {
	e := newAgentEnv(t)
	ctx := context.Background()

	issuedRec := doAs(t, e.app.CreatePairingCode, e.user, http.MethodPost,
		"/v1/agent/runners/pairing-codes", "", map[string]any{"projectId": e.project.ID})
	if issuedRec.Code != http.StatusCreated {
		t.Fatalf("issue pairing code = %d, want 201; body %s", issuedRec.Code, issuedRec.Body)
	}
	var issued createPairingCodeResponse
	if err := json.Unmarshal(issuedRec.Body.Bytes(), &issued); err != nil {
		t.Fatalf("decode pairing code: %v", err)
	}
	if !strings.HasPrefix(issued.Code, pairingTokenPrefix) {
		t.Errorf("code = %q, want the ttpc_ prefix", issued.Code)
	}
	if issued.ProjectID != e.project.ID {
		t.Errorf("projectId = %q, want %q", issued.ProjectID, e.project.ID)
	}

	pairRec := e.do(t, http.HandlerFunc(e.handler.Pair), http.MethodPost, "/v1/agent/pair", "", "", map[string]any{
		"code": issued.Code, "name": "vps", "provider": "echo",
	})
	if pairRec.Code != http.StatusCreated {
		t.Fatalf("pair = %d, want 201; body %s", pairRec.Code, pairRec.Body)
	}
	var paired pairResponse
	if err := json.Unmarshal(pairRec.Body.Bytes(), &paired); err != nil {
		t.Fatalf("decode pair: %v", err)
	}
	if paired.Runner.ProjectID == nil || *paired.Runner.ProjectID != e.project.ID {
		t.Fatalf("paired project = %v, want %q", paired.Runner.ProjectID, e.project.ID)
	}
	if !strings.HasPrefix(paired.Token, tokenPrefix) {
		t.Errorf("token = %q, want a ttag_ runner token", paired.Token)
	}

	// An older run in another project must not hide this project's work from a
	// scoped machine.
	otherProject := &entities.Project{
		KeyPrefix: "ZZ", Name: "Other", MemberIDs: []entities.UserID{e.user.ID}, AgentEnabled: true,
	}
	if err := entities.NewProjectRepository(e.database.DB).Create(ctx, otherProject); err != nil {
		t.Fatalf("create other project: %v", err)
	}
	otherItem := &entities.WorkItem{
		ProjectID: otherProject.ID, Key: "ZZ-1", Title: "Elsewhere",
		Status: entities.WorkItemStatusInProgress, Priority: entities.PriorityLow,
	}
	if err := entities.NewWorkItemRepository(e.database.DB).Create(ctx, otherItem); err != nil {
		t.Fatalf("create other item: %v", err)
	}
	if err := e.repo.CreateRun(ctx, &entities.AgentRun{
		WorkItemID: otherItem.ID, ProjectID: otherProject.ID, RequestedBy: e.user.ID,
		TaskDocument: `{}`, CreatedAt: entities.Timestamp(1),
	}); err != nil {
		t.Fatalf("create other run: %v", err)
	}
	own := e.queueRun(t)

	claim := e.do(t, e.authed(e.handler.Claim), http.MethodPost, "/v1/agent/runs/claim", paired.Token, "", map[string]any{"waitSeconds": 0})
	if claim.Code != http.StatusOK {
		t.Fatalf("scoped claim = %d, want 200; body %s", claim.Code, claim.Body)
	}
	var claimed struct {
		Run runnerRunResponse `json:"run"`
	}
	if err := json.Unmarshal(claim.Body.Bytes(), &claimed); err != nil {
		t.Fatalf("decode claim: %v", err)
	}
	if claimed.Run.ID != own.ID {
		t.Errorf("scoped machine claimed %s, want its project's run %s", claimed.Run.ID, own.ID)
	}

	reuse := e.do(t, http.HandlerFunc(e.handler.Pair), http.MethodPost, "/v1/agent/pair", "", "", map[string]any{
		"code": issued.Code, "name": "second",
	})
	if reuse.Code != http.StatusUnauthorized {
		t.Errorf("reusing a spent code = %d, want 401; body %s", reuse.Code, reuse.Body)
	}

	expiredCode := pairingTokenPrefix + "expired"
	if err := e.repo.CreatePairingCode(ctx, HashRunnerToken(expiredCode), e.project.ID, e.user.ID, entities.Timestamp(1)); err != nil {
		t.Fatalf("create expired code: %v", err)
	}
	expired := e.do(t, http.HandlerFunc(e.handler.Pair), http.MethodPost, "/v1/agent/pair", "", "", map[string]any{
		"code": expiredCode, "name": "late",
	})
	if expired.Code != http.StatusUnauthorized {
		t.Errorf("an expired code = %d, want 401; body %s", expired.Code, expired.Body)
	}

	missingName := e.do(t, http.HandlerFunc(e.handler.Pair), http.MethodPost, "/v1/agent/pair", "", "", map[string]any{
		"code": issued.Code,
	})
	if missingName.Code != http.StatusBadRequest {
		t.Errorf("pairing without a name = %d, want 400; body %s", missingName.Code, missingName.Body)
	}
}

// A project with the agent switch off refuses dispatch even for a member, so the
// flag is a server rule rather than only a greyed-out button.
func TestDispatchRequiresTheProjectAgentSwitch(t *testing.T) {
	e := newAgentEnv(t)
	e.project.AgentEnabled = false
	if err := entities.NewProjectRepository(e.database.DB).Update(context.Background(), e.project); err != nil {
		t.Fatalf("disable agent: %v", err)
	}

	rec := doAs(t, e.app.Dispatch, e.user, http.MethodPost, "/v1/agent/runs", "", map[string]any{"workItemId": e.item.ID})
	if rec.Code != http.StatusConflict {
		t.Errorf("dispatch on a disabled project = %d, want 409; body %s", rec.Code, rec.Body)
	}
}
