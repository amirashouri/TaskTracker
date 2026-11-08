package agent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/amirashouri/tasktracker-server/internal/auth"
	"github.com/amirashouri/tasktracker-server/internal/entities"
	"github.com/amirashouri/tasktracker-server/internal/httpx"
)

const (
	// defaultRunsPage is how many runs an item's history shows at once.
	defaultRunsPage = 20
	maxRunsPage     = 50
	// eventPageSize is the most progress lines one poll returns. The client
	// polls with afterSeq, so a long run pages rather than truncating.
	eventPageSize = 200
	// maxRunnerNameLength matches the profile fields' limit: a name is a label
	// on a settings row, not a free-form note.
	maxRunnerNameLength = 120
	// maxIdempotencyKeyLength bounds the client's per-dispatch key. It is an
	// opaque value, not a note; a longer one is a client bug, not storage to keep.
	maxIdempotencyKeyLength = 200
	// pairingCodeTTL is how long an admin has to hand a code to the machine that
	// will pair. Short enough that a leaked code is stale, long enough to walk to
	// another terminal and paste it.
	pairingCodeTTL = 10 * time.Minute
)

// Membership is the one question this surface asks about a project: may this
// user see and dispatch its work? sync.Engine satisfies it, so the check is the
// same query the change feed and the attachment API use.
type Membership interface {
	IsMember(ctx context.Context, userID, projectID string) (bool, error)
}

// AppHandler serves the app-facing half of agent runs: dispatching a work item,
// reading progress, cancelling, and managing the caller's paired machines.
//
// Everything here authenticates a user JWT and is scoped by project membership.
// The worker half lives in Handler and accepts only an opaque runner token; the
// two credentials are never interchangeable.
type AppHandler struct {
	repo      *entities.AgentRepository
	workItems *entities.WorkItemRepository
	projects  *entities.ProjectRepository
	comments  *entities.CommentRepository
	users     *entities.UserRepository
	members   Membership
}

func NewAppHandler(
	repo *entities.AgentRepository,
	workItems *entities.WorkItemRepository,
	projects *entities.ProjectRepository,
	comments *entities.CommentRepository,
	users *entities.UserRepository,
	members Membership,
) *AppHandler {
	return &AppHandler{
		repo:      repo,
		workItems: workItems,
		projects:  projects,
		comments:  comments,
		users:     users,
		members:   members,
	}
}

// appRunResponse is the run as the app sees it: no task snapshot and no lease,
// which are the worker's business. The Kotlin DTO declares `runnerId`,
// `startedAt` and `finishedAt` with defaults, so those keys may be omitted; every
// other key is required and always present.
type appRunResponse struct {
	ID              entities.AgentRunID     `json:"id"`
	WorkItemID      entities.WorkItemID     `json:"workItemId"`
	ProjectID       entities.ProjectID      `json:"projectId"`
	Status          entities.AgentRunStatus `json:"status"`
	RequestedBy     entities.UserID         `json:"requestedBy"`
	RunnerID        *entities.AgentRunnerID `json:"runnerId,omitempty"`
	CancelRequested bool                    `json:"cancelRequested"`
	CreatedAt       entities.Timestamp      `json:"createdAt"`
	StartedAt       *entities.Timestamp     `json:"startedAt,omitempty"`
	FinishedAt      *entities.Timestamp     `json:"finishedAt,omitempty"`
	Summary         string                  `json:"summary"`
	Error           string                  `json:"error"`
}

func appRunFrom(run entities.AgentRun) appRunResponse {
	return appRunResponse{
		ID:              run.ID,
		WorkItemID:      run.WorkItemID,
		ProjectID:       run.ProjectID,
		Status:          run.Status,
		RequestedBy:     run.RequestedBy,
		RunnerID:        run.RunnerID,
		CancelRequested: run.CancelRequested,
		CreatedAt:       run.CreatedAt,
		StartedAt:       run.StartedAt,
		FinishedAt:      run.FinishedAt,
		Summary:         run.Summary,
		Error:           run.Error,
	}
}

type appEventResponse struct {
	Seq       int64              `json:"seq"`
	Kind      string             `json:"kind"`
	Message   string             `json:"message"`
	Payload   string             `json:"payload"`
	CreatedAt entities.Timestamp `json:"createdAt"`
}

type appRunnerResponse struct {
	ID         entities.AgentRunnerID `json:"id"`
	Name       string                 `json:"name"`
	Provider   string                 `json:"provider"`
	ModelLabel string                 `json:"modelLabel"`
	LastSeenAt *entities.Timestamp    `json:"lastSeenAt,omitempty"`
	RevokedAt  *entities.Timestamp    `json:"revokedAt,omitempty"`
	// PausedAt is the settings screen's stop state. It is additive: the client
	// that reads this route declares it with a default, and the build shipped
	// before this key never called the route at all.
	PausedAt  *entities.Timestamp `json:"pausedAt,omitempty"`
	CreatedAt entities.Timestamp  `json:"createdAt"`
}

func appRunnerFrom(runner entities.AgentRunner) appRunnerResponse {
	return appRunnerResponse{
		ID:         runner.ID,
		Name:       runner.Name,
		Provider:   runner.Provider,
		ModelLabel: runner.ModelLabel,
		LastSeenAt: runner.LastSeenAt,
		RevokedAt:  runner.RevokedAt,
		PausedAt:   runner.PausedAt,
		CreatedAt:  runner.CreatedAt,
	}
}

// dispatchRequest names the item to hand to an agent. IdempotencyKey is
// optional, so a client that predates it keeps working; a client that sends one
// can retry a lost dispatch without creating a second run.
type dispatchRequest struct {
	WorkItemID     entities.WorkItemID `json:"workItemId"`
	IdempotencyKey string              `json:"idempotencyKey"`
}

type runResponse struct {
	Run appRunResponse `json:"run"`
}

// Dispatch queues a work item for an agent.
//
// Three preconditions, checked in the order that gives the most useful answer:
// the item exists, the caller is a member of its project, and it is in progress.
// A second live run is refused by the repository's partial index rather than by
// a read here, because two devices can pass a read at the same instant.
func (h *AppHandler) Dispatch(w http.ResponseWriter, r *http.Request) {
	userID, ok := callerID(r)
	if !ok {
		httpx.WriteError(w, http.StatusUnauthorized, "unauthenticated")
		return
	}

	var req dispatchRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil || req.WorkItemID == "" {
		httpx.WriteError(w, http.StatusBadRequest, "workItemId is required")
		return
	}
	idempotencyKey := strings.TrimSpace(req.IdempotencyKey)
	if len(idempotencyKey) > maxIdempotencyKeyLength {
		httpx.WriteError(w, http.StatusBadRequest, "idempotencyKey is too long")
		return
	}

	ctx := r.Context()
	item, err := h.workItems.GetByID(ctx, req.WorkItemID)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "could not read the work item")
		return
	}
	if item == nil {
		httpx.WriteError(w, http.StatusNotFound, "work item not found")
		return
	}
	if !h.requireMember(ctx, w, userID, item.ProjectID) {
		return
	}
	if item.Status != entities.WorkItemStatusInProgress {
		httpx.WriteError(w, http.StatusConflict, "work item is not in progress")
		return
	}

	project, err := h.projects.GetByID(ctx, item.ProjectID)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "could not read the project")
		return
	}
	if project == nil {
		httpx.WriteError(w, http.StatusNotFound, "project not found")
		return
	}

	if !project.AgentEnabled {
		httpx.WriteError(w, http.StatusConflict, "agent is not enabled for this project")
		return
	}

	// The run id is minted before the brief because the brief names it: a worker
	// that recovers after a lost claim can tell which run it is holding.
	runID := entities.NewAgentRunID()
	document, err := h.buildContextGraph(ctx, runID, *item, *project)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "could not prepare the task")
		return
	}

	run := &entities.AgentRun{
		ID:             runID,
		WorkItemID:     item.ID,
		ProjectID:      item.ProjectID,
		RequestedBy:    userID,
		TaskDocument:   document,
		IdempotencyKey: idempotencyKey,
	}
	stored, created, err := h.repo.CreateRunIdempotent(ctx, run)
	if err != nil {
		if errors.Is(err, entities.ErrAgentRunActive) {
			httpx.WriteError(w, http.StatusConflict, "an agent run is already active for this work item")
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, "could not start the run")
		return
	}

	// A replay returns the run the first request created, so the client can
	// recover from a lost response without starting duplicate work.
	status := http.StatusCreated
	if !created {
		status = http.StatusOK
	}
	httpx.WriteJSON(w, status, runResponse{Run: appRunFrom(*stored)})
}

type runsResponse struct {
	Runs []appRunResponse `json:"runs"`
}

// ListRuns returns an item's recent runs, newest first. The app calls it when the
// editor opens: a run that finished while the app was closed is normal, and this
// is how its result is recovered.
func (h *AppHandler) ListRuns(w http.ResponseWriter, r *http.Request) {
	userID, ok := callerID(r)
	if !ok {
		httpx.WriteError(w, http.StatusUnauthorized, "unauthenticated")
		return
	}

	query := r.URL.Query()
	workItemID := entities.WorkItemID(strings.TrimSpace(query.Get("workItemId")))
	if workItemID == "" {
		httpx.WriteError(w, http.StatusBadRequest, "workItemId is required")
		return
	}

	limit := defaultRunsPage
	if raw := query.Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 {
			httpx.WriteError(w, http.StatusBadRequest, "invalid limit")
			return
		}
		if parsed < maxRunsPage {
			limit = parsed
		} else {
			limit = maxRunsPage
		}
	}

	ctx := r.Context()
	item, err := h.workItems.GetByID(ctx, workItemID)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "could not read the work item")
		return
	}
	if item == nil {
		httpx.WriteError(w, http.StatusNotFound, "work item not found")
		return
	}
	if !h.requireMember(ctx, w, userID, item.ProjectID) {
		return
	}

	runs, err := h.repo.ListRunsByWorkItem(ctx, workItemID, limit)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "could not read the runs")
		return
	}
	response := runsResponse{Runs: make([]appRunResponse, 0, len(runs))}
	for _, run := range runs {
		response.Runs = append(response.Runs, appRunFrom(*run))
	}
	httpx.WriteJSON(w, http.StatusOK, response)
}

// EventPageSize is exported for the client and its tests: one poll returns at
// most this many progress lines, and `hasMore` says whether to poll again at once.
const EventPageSize = eventPageSize

type runDetailResponse struct {
	Run     appRunResponse     `json:"run"`
	Events  []appEventResponse `json:"events"`
	HasMore bool               `json:"hasMore"`
}

// GetRun is the progress sheet's one round trip: the run's state plus the events
// after the caller's cursor. `hasMore` is always present — the client does not
// default it, because a missing value would silently end a page early.
func (h *AppHandler) GetRun(w http.ResponseWriter, r *http.Request) {
	userID, ok := callerID(r)
	if !ok {
		httpx.WriteError(w, http.StatusUnauthorized, "unauthenticated")
		return
	}

	ctx := r.Context()
	run, err := h.repo.GetRun(ctx, entities.AgentRunID(r.PathValue("id")))
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "could not read the run")
		return
	}
	if run == nil {
		httpx.WriteError(w, http.StatusNotFound, "agent run not found")
		return
	}
	if !h.requireMember(ctx, w, userID, run.ProjectID) {
		return
	}

	afterSeq := int64(0)
	if raw := r.URL.Query().Get("afterSeq"); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || parsed < 0 {
			httpx.WriteError(w, http.StatusBadRequest, "invalid afterSeq")
			return
		}
		afterSeq = parsed
	}

	// One extra row is the answer to "is there another page", rather than a
	// second COUNT that could disagree with the page it describes.
	events, err := h.repo.ListEventsAfter(ctx, run.ID, afterSeq, eventPageSize+1)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "could not read the run's progress")
		return
	}
	hasMore := len(events) > eventPageSize
	if hasMore {
		events = events[:eventPageSize]
	}
	page := make([]appEventResponse, 0, len(events))
	for _, event := range events {
		page = append(page, appEventResponse{
			Seq:       event.Seq,
			Kind:      event.Kind,
			Message:   event.Message,
			Payload:   event.Payload,
			CreatedAt: event.CreatedAt,
		})
	}

	httpx.WriteJSON(w, http.StatusOK, runDetailResponse{Run: appRunFrom(*run), Events: page, HasMore: hasMore})
}

// Cancel asks the runner to stop. It takes effect at the worker's next heartbeat,
// so the response is "requested", not "stopped".
func (h *AppHandler) Cancel(w http.ResponseWriter, r *http.Request) {
	userID, ok := callerID(r)
	if !ok {
		httpx.WriteError(w, http.StatusUnauthorized, "unauthenticated")
		return
	}

	ctx := r.Context()
	existing, err := h.repo.GetRun(ctx, entities.AgentRunID(r.PathValue("id")))
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "could not read the run")
		return
	}
	if existing == nil {
		httpx.WriteError(w, http.StatusNotFound, "agent run not found")
		return
	}
	if !h.requireMember(ctx, w, userID, existing.ProjectID) {
		return
	}

	run, err := h.repo.RequestCancel(ctx, existing.ID)
	if err != nil {
		if errors.Is(err, entities.ErrAgentRunNotFound) {
			httpx.WriteError(w, http.StatusNotFound, "agent run not found")
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, "could not cancel the run")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, runResponse{Run: appRunFrom(*run)})
}

type registerRunnerRequest struct {
	Name       string `json:"name"`
	Provider   string `json:"provider"`
	ModelLabel string `json:"modelLabel"`
}

type registerRunnerResponse struct {
	Runner appRunnerResponse `json:"runner"`
	// Token is returned exactly once. It is stored as a hash, so a lost response
	// means registering the machine again, not recovering the value.
	Token string `json:"token"`
}

// RegisterRunner pairs a machine. The caller mints nothing: the server generates
// the token, stores only its hash, and shows the plaintext once.
func (h *AppHandler) RegisterRunner(w http.ResponseWriter, r *http.Request) {
	userID, ok := callerID(r)
	if !ok {
		httpx.WriteError(w, http.StatusUnauthorized, "unauthenticated")
		return
	}

	var req registerRunnerRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	name := strings.TrimSpace(req.Name)
	if name == "" {
		httpx.WriteError(w, http.StatusBadRequest, "name is required")
		return
	}
	provider := strings.TrimSpace(req.Provider)
	modelLabel := strings.TrimSpace(req.ModelLabel)
	for _, field := range []string{name, provider, modelLabel} {
		if utf8.RuneCountInString(field) > maxRunnerNameLength {
			httpx.WriteError(w, http.StatusBadRequest, "name, provider and modelLabel must be at most 120 characters")
			return
		}
	}

	token, err := newRunnerToken()
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "could not create a runner token")
		return
	}

	runner := &entities.AgentRunner{
		UserID:     userID,
		Name:       name,
		Provider:   provider,
		ModelLabel: modelLabel,
	}
	if err := h.repo.RegisterRunner(r.Context(), runner, HashRunnerToken(token)); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "could not register the runner")
		return
	}

	httpx.WriteJSON(w, http.StatusCreated, registerRunnerResponse{
		Runner: appRunnerFrom(*runner),
		Token:  token,
	})
}

type runnersResponse struct {
	Runners []appRunnerResponse `json:"runners"`
}

// ListRunners returns the caller's machines, revoked ones included, so the
// settings screen can show history rather than forgetting a machine it retired.
func (h *AppHandler) ListRunners(w http.ResponseWriter, r *http.Request) {
	userID, ok := callerID(r)
	if !ok {
		httpx.WriteError(w, http.StatusUnauthorized, "unauthenticated")
		return
	}

	runners, err := h.repo.ListRunners(r.Context(), userID)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "could not read the runners")
		return
	}
	response := runnersResponse{Runners: make([]appRunnerResponse, 0, len(runners))}
	for _, runner := range runners {
		response.Runners = append(response.Runners, appRunnerFrom(*runner))
	}
	httpx.WriteJSON(w, http.StatusOK, response)
}

type runnerResponse struct {
	Runner appRunnerResponse `json:"runner"`
}

// StopRunner pauses a machine: it keeps its token, claims no new work, and has
// its live run asked to cancel. The route is admin-gated in main.go, because
// stopping the fleet is an administrative action rather than a member's own.
func (h *AppHandler) StopRunner(w http.ResponseWriter, r *http.Request) {
	h.setRunnerPaused(w, r, true)
}

// StartRunner is the reversible half: the same machine begins claiming again.
// The worker must still be running `tt agent run` — this switch gates the queue,
// it cannot launch a process on somebody else's computer.
func (h *AppHandler) StartRunner(w http.ResponseWriter, r *http.Request) {
	h.setRunnerPaused(w, r, false)
}

func (h *AppHandler) setRunnerPaused(w http.ResponseWriter, r *http.Request, paused bool) {
	id := entities.AgentRunnerID(strings.TrimSpace(r.PathValue("id")))
	if id == "" {
		httpx.WriteError(w, http.StatusBadRequest, "runner id is required")
		return
	}

	runner, err := h.repo.SetRunnerPaused(r.Context(), id, paused)
	if errors.Is(err, entities.ErrAgentRunnerNotFound) {
		httpx.WriteError(w, http.StatusNotFound, "agent runner not found")
		return
	}
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "could not update the runner")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, runnerResponse{Runner: appRunnerFrom(*runner)})
}

// RevokeRunner retires one of the caller's machines. Revocation is a date on the
// row, so the runs it reported stay attributed to it.
func (h *AppHandler) RevokeRunner(w http.ResponseWriter, r *http.Request) {
	userID, ok := callerID(r)
	if !ok {
		httpx.WriteError(w, http.StatusUnauthorized, "unauthenticated")
		return
	}

	err := h.repo.RevokeRunner(r.Context(), entities.AgentRunnerID(r.PathValue("id")), userID)
	if errors.Is(err, entities.ErrAgentRunnerNotFound) {
		httpx.WriteError(w, http.StatusNotFound, "agent runner not found")
		return
	}
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "could not revoke the runner")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type createPairingCodeRequest struct {
	ProjectID entities.ProjectID `json:"projectId"`
}

type createPairingCodeResponse struct {
	// Code is shown once. The server stores only its hash, so a lost response
	// means issuing a new code, not recovering this one.
	Code      string             `json:"code"`
	ProjectID entities.ProjectID `json:"projectId"`
	ExpiresAt entities.Timestamp `json:"expiresAt"`
}

// CreatePairingCode issues a short-lived, single-use code for one project.
//
// It is the app-facing half of pairing: a user JWT and project membership decide
// who may invite a machine, and the code itself is what the machine redeems over
// the unauthenticated Pair route. The code is not the long-lived runner token;
// that is minted only when the code is spent.
func (h *AppHandler) CreatePairingCode(w http.ResponseWriter, r *http.Request) {
	userID, ok := callerID(r)
	if !ok {
		httpx.WriteError(w, http.StatusUnauthorized, "unauthenticated")
		return
	}

	var req createPairingCodeRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil || req.ProjectID == "" {
		httpx.WriteError(w, http.StatusBadRequest, "projectId is required")
		return
	}

	ctx := r.Context()
	project, err := h.projects.GetByID(ctx, req.ProjectID)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "could not read the project")
		return
	}
	if project == nil {
		httpx.WriteError(w, http.StatusNotFound, "project not found")
		return
	}
	if !h.requireMember(ctx, w, userID, project.ID) {
		return
	}

	code, err := newPairingCode()
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "could not create a pairing code")
		return
	}
	expiresAt := entities.Timestamp(time.Now().Add(pairingCodeTTL).Truncate(time.Second).UnixMilli())
	if err := h.repo.CreatePairingCode(ctx, HashRunnerToken(code), project.ID, userID, expiresAt); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "could not create a pairing code")
		return
	}

	httpx.WriteJSON(w, http.StatusCreated, createPairingCodeResponse{
		Code:      code,
		ProjectID: project.ID,
		ExpiresAt: expiresAt,
	})
}

// requireMember answers the request itself on failure, so every caller writes the
// same refusal and none of them can forget it.
func (h *AppHandler) requireMember(ctx context.Context, w http.ResponseWriter, userID entities.UserID, projectID entities.ProjectID) bool {
	member, err := h.members.IsMember(ctx, string(userID), string(projectID))
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "could not verify project membership")
		return false
	}
	if !member {
		httpx.WriteError(w, http.StatusForbidden, "not a member of this project")
		return false
	}
	return true
}

func callerID(r *http.Request) (entities.UserID, bool) {
	userID, ok := auth.UserFrom(r.Context())
	return entities.UserID(userID), ok
}

// newRunnerToken mints an opaque machine credential. 32 bytes of randomness is
// the whole secret; the prefix only makes it recognisable in a log.
func newRunnerToken() (string, error) {
	return newOpaqueToken(tokenPrefix)
}

// newPairingCode mints a short-lived pairing secret, distinct from the runner
// token so a leaked value is recognisable as the weaker, expiring one.
func newPairingCode() (string, error) {
	return newOpaqueToken(pairingTokenPrefix)
}

func newOpaqueToken(prefix string) (string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return prefix + hex.EncodeToString(raw[:]), nil
}
