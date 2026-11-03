package agent

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/amirashouri/tasktracker-server/internal/entities"
	"github.com/amirashouri/tasktracker-server/internal/httpx"
)

const (
	// maxClaimWaitSeconds bounds a long poll. It is above the composition root's
	// WriteTimeout on purpose — Claim clears that deadline for its own response,
	// because a 25-second poll and a 15-second write timeout cannot both be true.
	maxClaimWaitSeconds = 30
	// defaultClaimWaitSeconds is what an omitted waitSeconds means: wait, rather
	// than answer "nothing" instantly and have the worker spin.
	defaultClaimWaitSeconds = 25
	// maxEventsPerBatch keeps one request from carrying an unbounded log. A
	// worker batches its stream and sends what is here.
	maxEventsPerBatch = 200
)

// Handler serves the runner API. It owns no lifecycle rules the repository does
// not already enforce; it is the translation between HTTP and the queue.
type Handler struct {
	repo     *entities.AgentRepository
	leaseTTL time.Duration
	// now and pollInterval are seams for tests: a claim loop that can only be
	// exercised in real seconds is a test nobody keeps fast.
	now          func() time.Time
	pollInterval time.Duration
}

func NewHandler(repo *entities.AgentRepository, leaseTTL time.Duration) *Handler {
	// Config validates this as positive; the fallback keeps a handler built in a
	// test from issuing leases that expire before the response is written.
	if leaseTTL <= 0 {
		leaseTTL = time.Minute
	}
	return &Handler{repo: repo, leaseTTL: leaseTTL, now: time.Now, pollInterval: time.Second}
}

// leaseUntil is the instant a claim or a batch is good until. It is truncated to
// whole seconds, the resolution the rest of the entity wire format carries.
func (h *Handler) leaseUntil() entities.Timestamp {
	return entities.Timestamp(h.now().Add(h.leaseTTL).Truncate(time.Second).UnixMilli())
}

// runnerRunResponse is the run as a worker sees it. It differs from the app's
// shape by carrying the task snapshot and the lease, which are the worker's
// business and not the app's.
type runnerRunResponse struct {
	ID              entities.AgentRunID     `json:"id"`
	WorkItemID      entities.WorkItemID     `json:"workItemId"`
	ProjectID       entities.ProjectID      `json:"projectId"`
	Status          entities.AgentRunStatus `json:"status"`
	CancelRequested bool                    `json:"cancelRequested"`
	TaskDocument    string                  `json:"taskDocument,omitempty"`
	LeaseExpiresAt  *entities.Timestamp     `json:"leaseExpiresAt,omitempty"`
	CreatedAt       entities.Timestamp      `json:"createdAt"`
	StartedAt       *entities.Timestamp     `json:"startedAt,omitempty"`
	FinishedAt      *entities.Timestamp     `json:"finishedAt,omitempty"`
	Summary         string                  `json:"summary"`
	Error           string                  `json:"error"`
}

// newRunnerRun copies a run for the wire. includeDocument is false on finish,
// where the worker already has the snapshot and re-sending it is noise.
func newRunnerRun(run entities.AgentRun, includeDocument bool) runnerRunResponse {
	response := runnerRunResponse{
		ID:              run.ID,
		WorkItemID:      run.WorkItemID,
		ProjectID:       run.ProjectID,
		Status:          run.Status,
		CancelRequested: run.CancelRequested,
		LeaseExpiresAt:  run.LeaseExpiresAt,
		CreatedAt:       run.CreatedAt,
		StartedAt:       run.StartedAt,
		FinishedAt:      run.FinishedAt,
		Summary:         run.Summary,
		Error:           run.Error,
	}
	if includeDocument {
		response.TaskDocument = run.TaskDocument
	}
	return response
}

type claimRequest struct {
	// A pointer so an omitted field means "use the default" while an explicit 0
	// means "answer now" — the difference between a poll and a probe.
	WaitSeconds *int `json:"waitSeconds"`
}

type claimResponse struct {
	Run runnerRunResponse `json:"run"`
}

// Claim leases the oldest queued run for this runner's user, waiting up to
// waitSeconds for one to appear.
//
// The wait is a re-try loop rather than a database notification: both drivers
// support the atomic claim, and neither offers a portable way to block on a row.
// One statement per second is cheaper than a new transport, and the long poll is
// bounded so a client always gets an answer.
func (h *Handler) Claim(w http.ResponseWriter, r *http.Request) {
	runner, ok := RunnerFrom(r.Context())
	if !ok {
		httpx.WriteError(w, http.StatusUnauthorized, "unauthenticated")
		return
	}

	var req claimRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil && !errors.Is(err, io.EOF) {
		httpx.WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	waitSeconds := defaultClaimWaitSeconds
	if req.WaitSeconds != nil {
		waitSeconds = *req.WaitSeconds
	}
	if waitSeconds < 0 {
		waitSeconds = 0
	}
	if waitSeconds > maxClaimWaitSeconds {
		waitSeconds = maxClaimWaitSeconds
	}

	// The server's WriteTimeout is shorter than a long poll. Clear it for this
	// response only; a ResponseWriter that cannot be controlled (a test recorder)
	// reports an error we do not need to act on, because the poll is still
	// answered by the configured deadline.
	_ = http.NewResponseController(w).SetWriteDeadline(time.Time{})

	deadline := h.now().Add(time.Duration(waitSeconds) * time.Second)
	for {
		run, err := h.repo.ClaimRun(r.Context(), runner.ID, runner.UserID, h.leaseUntil())
		if err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "could not claim a run")
			return
		}
		if run != nil {
			// Best effort: liveness metadata must not fail a claim that succeeded.
			_ = h.repo.TouchRunner(r.Context(), runner.ID)
			httpx.WriteJSON(w, http.StatusOK, claimResponse{Run: newRunnerRun(*run, true)})
			return
		}

		if waitSeconds == 0 || !h.now().Before(deadline) {
			_ = h.repo.TouchRunner(r.Context(), runner.ID)
			w.WriteHeader(http.StatusNoContent)
			return
		}

		select {
		case <-r.Context().Done():
			return
		case <-time.After(h.pollInterval):
		}
	}
}

// eventInput is one reported step. Payload is an opaque JSON string: the server
// never interprets it, so a new event kind cannot change this shape.
type eventInput struct {
	Kind    string `json:"kind"`
	Message string `json:"message"`
	Payload string `json:"payload"`
}

type appendEventsRequest struct {
	Events []eventInput `json:"events"`
}

type appendEventsResponse struct {
	Accepted int   `json:"accepted"`
	NextSeq  int64 `json:"nextSeq"`
}

// AppendEvents writes a batch of progress and renews the lease. The batch is also
// a heartbeat, so a long tool call is not mistaken for a dead runner.
func (h *Handler) AppendEvents(w http.ResponseWriter, r *http.Request) {
	runner, ok := RunnerFrom(r.Context())
	if !ok {
		httpx.WriteError(w, http.StatusUnauthorized, "unauthenticated")
		return
	}

	var req appendEventsRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if len(req.Events) == 0 {
		httpx.WriteError(w, http.StatusBadRequest, "no events to append")
		return
	}
	if len(req.Events) > maxEventsPerBatch {
		httpx.WriteError(w, http.StatusBadRequest, "too many events in one request")
		return
	}

	events := make([]entities.AgentRunEvent, 0, len(req.Events))
	for _, event := range req.Events {
		if strings.TrimSpace(event.Kind) == "" {
			httpx.WriteError(w, http.StatusBadRequest, "event kind is required")
			return
		}
		events = append(events, entities.AgentRunEvent{
			Kind:    event.Kind,
			Message: event.Message,
			Payload: event.Payload,
		})
	}

	lastSeq, err := h.repo.AppendEvents(r.Context(), entities.AgentRunID(r.PathValue("id")), runner.ID, h.leaseUntil(), events)
	switch {
	case errors.Is(err, entities.ErrAgentRunNotClaimed):
		httpx.WriteError(w, http.StatusConflict, "run is not claimed by this runner")
		return
	case err != nil:
		httpx.WriteError(w, http.StatusInternalServerError, "could not record progress")
		return
	}

	httpx.WriteJSON(w, http.StatusOK, appendEventsResponse{Accepted: len(events), NextSeq: lastSeq})
}

type heartbeatResponse struct {
	CancelRequested bool                `json:"cancelRequested"`
	LeaseExpiresAt  *entities.Timestamp `json:"leaseExpiresAt,omitempty"`
}

// Heartbeat renews the lease and reports whether the user has asked to cancel.
//
// Cancel is read here rather than pushed to the worker: the server cannot reach
// a process on another machine, so the worker has to poll for the request.
func (h *Handler) Heartbeat(w http.ResponseWriter, r *http.Request) {
	runner, ok := RunnerFrom(r.Context())
	if !ok {
		httpx.WriteError(w, http.StatusUnauthorized, "unauthenticated")
		return
	}

	cancelRequested, lease, err := h.repo.HeartbeatRun(r.Context(), entities.AgentRunID(r.PathValue("id")), runner.ID, h.leaseUntil())
	switch {
	case errors.Is(err, entities.ErrAgentRunNotClaimed):
		httpx.WriteError(w, http.StatusConflict, "run is not claimed by this runner")
		return
	case err != nil:
		httpx.WriteError(w, http.StatusInternalServerError, "could not renew the lease")
		return
	}

	httpx.WriteJSON(w, http.StatusOK, heartbeatResponse{CancelRequested: cancelRequested, LeaseExpiresAt: lease})
}

type finishRequest struct {
	Outcome string `json:"outcome"`
	Summary string `json:"summary"`
	Error   string `json:"error"`
}

type finishResponse struct {
	Run runnerRunResponse `json:"run"`
}

// Finish records the terminal outcome. The status is chosen from the worker's
// outcome word, and an unknown word is a 400 rather than a guess: a run marked
// `succeeded` when the worker meant to cancel it is a lie in the history.
func (h *Handler) Finish(w http.ResponseWriter, r *http.Request) {
	runner, ok := RunnerFrom(r.Context())
	if !ok {
		httpx.WriteError(w, http.StatusUnauthorized, "unauthenticated")
		return
	}

	var req finishRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	status, ok := finishStatus(req.Outcome)
	if !ok {
		httpx.WriteError(w, http.StatusBadRequest, "outcome must be succeeded, failed or cancelled")
		return
	}

	run, err := h.repo.FinishRun(r.Context(), entities.AgentRunID(r.PathValue("id")), runner.ID, status, req.Summary, req.Error)
	switch {
	case errors.Is(err, entities.ErrAgentRunNotClaimed):
		httpx.WriteError(w, http.StatusConflict, "run is not claimed by this runner")
		return
	case errors.Is(err, entities.ErrAgentRunNotFound):
		httpx.WriteError(w, http.StatusNotFound, "agent run not found")
		return
	case err != nil:
		httpx.WriteError(w, http.StatusInternalServerError, "could not finish the run")
		return
	}

	httpx.WriteJSON(w, http.StatusOK, finishResponse{Run: newRunnerRun(*run, false)})
}

// RevokeSelf lets a machine retire itself.
//
// The app route revokes by id with a user JWT, which `tt agent disconnect` does
// not have: the CLI stores a runner token, not a password. This is the one
// revocation a runner token may perform, and it can only ever name the machine
// that presented it.
func (h *Handler) RevokeSelf(w http.ResponseWriter, r *http.Request) {
	runner, ok := RunnerFrom(r.Context())
	if !ok {
		httpx.WriteError(w, http.StatusUnauthorized, "unauthenticated")
		return
	}

	if err := h.repo.RevokeRunner(r.Context(), runner.ID, runner.UserID); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "could not revoke the runner")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// finishStatus maps the worker's outcome word to the stored status. The closed
// vocabulary is the same one the column's CHECK accepts.
func finishStatus(outcome string) (entities.AgentRunStatus, bool) {
	switch outcome {
	case string(entities.AgentRunSucceeded):
		return entities.AgentRunSucceeded, true
	case string(entities.AgentRunFailed):
		return entities.AgentRunFailed, true
	case string(entities.AgentRunCancelled):
		return entities.AgentRunCancelled, true
	default:
		return "", false
	}
}
