package entities

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/mattn/go-sqlite3"
)

// Errors the agent-run handlers answer with, kept here so the repository and
// the HTTP layer agree on what "already running" and "not yours" mean.
var (
	// ErrAgentRunActive is the one-live-run rule: a work item already has a
	// queued, claimed or running run. It is what the partial index
	// idx_agent_runs_one_live reports, translated from the driver's error.
	ErrAgentRunActive = errors.New("an agent run is already active for this work item")

	// ErrAgentRunNotFound is a missing run, for callers that need to tell it
	// apart from "not yours".
	ErrAgentRunNotFound = errors.New("agent run not found")

	// ErrAgentRunNotClaimed means the run is not held by this runner — it was
	// never claimed, another runner owns it, or it already finished. A runner
	// may only report on the run it holds.
	ErrAgentRunNotClaimed = errors.New("agent run is not claimed by this runner")

	// ErrAgentRunnerNotFound covers both a missing runner and one owned by
	// somebody else, so a revoke cannot be used to probe for ids.
	ErrAgentRunnerNotFound = errors.New("agent runner not found")
)

// AgentRunStatus is the run's lifecycle. The column's CHECK lists exactly these
// values, so the constant set is the schema's vocabulary and not a second
// opinion about it.
type AgentRunStatus string

const (
	AgentRunQueued    AgentRunStatus = "queued"
	AgentRunClaimed   AgentRunStatus = "claimed"
	AgentRunRunning   AgentRunStatus = "running"
	AgentRunSucceeded AgentRunStatus = "succeeded"
	AgentRunFailed    AgentRunStatus = "failed"
	AgentRunCancelled AgentRunStatus = "cancelled"
)

// IsLive reports whether the run still occupies its work item's one live slot.
// The same three statuses are the partial index's predicate.
func (s AgentRunStatus) IsLive() bool {
	return s == AgentRunQueued || s == AgentRunClaimed || s == AgentRunRunning
}

// IsTerminal reports whether the run is finished, one way or another.
func (s AgentRunStatus) IsTerminal() bool {
	return s == AgentRunSucceeded || s == AgentRunFailed || s == AgentRunCancelled
}

// AgentRunID, AgentRunnerID and the rest are distinct string types for the same
// reason the other identifiers are: a run id passed where a work item id belongs
// is a mistake the compiler should catch.
type AgentRunID string
type AgentRunnerID string

func NewAgentRunID() AgentRunID       { return AgentRunID(newVersion()) }
func NewAgentRunnerID() AgentRunnerID { return AgentRunnerID(newVersion()) }

// AgentRunner is one paired machine that may claim work.
//
// The token hash is deliberately absent: it exists only inside this table, and a
// model that carried it would eventually be marshalled somewhere. Registration
// takes the hash as an argument and lookup goes through RunnerByTokenHash.
type AgentRunner struct {
	ID     AgentRunnerID `json:"id"`
	UserID UserID        `json:"userId"`
	// ProjectID is the assignment a pairing code established. Nil is a legacy
	// machine-wide registration: it may claim the owner's runs in any project,
	// which is what the already-shipped CLI expects. A project-scoped machine
	// only ever sees work in its own project.
	ProjectID  *ProjectID `json:"projectId,omitempty"`
	Name       string     `json:"name"`
	Provider   string     `json:"provider"`
	ModelLabel string     `json:"modelLabel"`
	LastSeenAt *Timestamp `json:"lastSeenAt,omitempty"`
	RevokedAt  *Timestamp `json:"revokedAt,omitempty"`
	// PausedAt is the reversible stop. A paused machine keeps its token and can
	// be started again; RevokedAt is the permanent retirement.
	PausedAt  *Timestamp `json:"pausedAt,omitempty"`
	CreatedAt Timestamp  `json:"createdAt"`
}

// Revoked reports whether the runner may no longer claim or report.
func (r *AgentRunner) Revoked() bool { return r.RevokedAt != nil }

// Paused reports whether the runner is stopped: it may not claim new work,
// though it may still finish the run it already holds.
func (r *AgentRunner) Paused() bool { return r.PausedAt != nil }

// AgentRun is one dispatch of a work item to an agent.
//
// TaskDocument is an opaque JSON string: it is the snapshot the runner receives,
// built at dispatch time and re-served verbatim thereafter. It is a string
// rather than typed columns so a detail added later cannot change a response
// key set a pinned client is decoding.
type AgentRun struct {
	ID              AgentRunID     `json:"id"`
	WorkItemID      WorkItemID     `json:"workItemId"`
	ProjectID       ProjectID      `json:"projectId"`
	RequestedBy     UserID         `json:"requestedBy"`
	RunnerID        *AgentRunnerID `json:"runnerId,omitempty"`
	Status          AgentRunStatus `json:"status"`
	CancelRequested bool           `json:"cancelRequested"`
	TaskDocument    string         `json:"-"`
	LeaseExpiresAt  *Timestamp     `json:"-"`
	// IdempotencyKey is the client's per-dispatch key. It never leaves the
	// server: the run it names is returned as the normal run object. Empty
	// means the caller sent none, which is the pre-idempotency behaviour.
	IdempotencyKey string     `json:"-"`
	Summary        string     `json:"summary"`
	Error          string     `json:"error"`
	CreatedAt      Timestamp  `json:"createdAt"`
	StartedAt      *Timestamp `json:"startedAt,omitempty"`
	FinishedAt     *Timestamp `json:"finishedAt,omitempty"`
}

// AgentRunEvent is one line in the run's progress log.
//
// Seq is assigned by the server and is the client's cursor (`afterSeq`). Payload
// is an opaque JSON string so a new event kind or a new detail never changes the
// response shape.
type AgentRunEvent struct {
	Seq       int64     `json:"seq"`
	Kind      string    `json:"kind"`
	Message   string    `json:"message"`
	Payload   string    `json:"payload"`
	CreatedAt Timestamp `json:"createdAt"`
}

// AgentRepository is the only place the agent tables are queried.
type AgentRepository struct {
	db Querier
}

func NewAgentRepository(db Querier) *AgentRepository {
	return &AgentRepository{db: db}
}

const agentRunColumns = `id, work_item_id, project_id, requested_by, runner_id, status, cancel_requested, task_document, lease_expires_at, summary, error, created_at, started_at, finished_at, idempotency_key`

func scanAgentRun(row scanner, run *AgentRun) error {
	var runnerID sql.NullString
	var lease, started, finished sql.NullInt64

	if err := row.Scan(&run.ID, &run.WorkItemID, &run.ProjectID, &run.RequestedBy, &runnerID,
		&run.Status, &run.CancelRequested, &run.TaskDocument, &lease, &run.Summary, &run.Error,
		&run.CreatedAt, &started, &finished, &run.IdempotencyKey); err != nil {
		return err
	}

	if runnerID.Valid {
		id := AgentRunnerID(runnerID.String)
		run.RunnerID = &id
	}
	run.LeaseExpiresAt = timestampOrNil(lease)
	run.StartedAt = timestampOrNil(started)
	run.FinishedAt = timestampOrNil(finished)
	return nil
}

const agentRunnerColumns = `id, user_id, project_id, name, provider, model_label, last_seen_at, revoked_at, created_at, paused_at`

func scanAgentRunner(row scanner, runner *AgentRunner) error {
	var projectID sql.NullString
	var lastSeen, revoked, paused sql.NullInt64

	if err := row.Scan(&runner.ID, &runner.UserID, &projectID, &runner.Name, &runner.Provider, &runner.ModelLabel,
		&lastSeen, &revoked, &runner.CreatedAt, &paused); err != nil {
		return err
	}
	if projectID.Valid {
		id := ProjectID(projectID.String)
		runner.ProjectID = &id
	}
	runner.LastSeenAt = timestampOrNil(lastSeen)
	runner.RevokedAt = timestampOrNil(revoked)
	runner.PausedAt = timestampOrNil(paused)
	return nil
}

// CreateRun queues a dispatch.
//
// The insert is the whole concurrency story: a second live run for the same work
// item is refused by idx_agent_runs_one_live, so two devices dispatching at once
// produce one run and one ErrAgentRunActive rather than two runs. The dispatch
// precondition (the item is in progress, the caller is a member) belongs to the
// handler, which is the layer that can read the work item and its project.
func (r *AgentRepository) CreateRun(ctx context.Context, run *AgentRun) error {
	run.ID = orNewID(run.ID, NewAgentRunID)
	if run.Status == "" {
		run.Status = AgentRunQueued
	}
	if run.CreatedAt.IsZero() {
		run.CreatedAt = Now()
	}

	_, err := r.db.ExecContext(ctx, `INSERT INTO agent_runs (`+agentRunColumns+`)
	          VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)`,
		run.ID, run.WorkItemID, run.ProjectID, run.RequestedBy, run.RunnerID, run.Status,
		run.CancelRequested, run.TaskDocument, run.LeaseExpiresAt, run.Summary, run.Error,
		run.CreatedAt, run.StartedAt, run.FinishedAt, run.IdempotencyKey)
	if err != nil && agentRunAlreadyActive(err) {
		return ErrAgentRunActive
	}
	return err
}

// CreateRunIdempotent records a dispatch at most once per (item, requester,
// key) and returns the stored run on a replay. The second return value is false
// when the caller is replaying, which is what lets the handler answer 200 rather
// than 201.
//
// The unique index is the authority, not the read below: two devices dispatching
// the same key together both pass the read, and the loser's insert fails. It then
// reads the winner's row, so a retry after a lost response returns the same run
// instead of a second one. A key that is absent keeps the pre-idempotency
// behaviour, including the one-live-run refusal.
func (r *AgentRepository) CreateRunIdempotent(ctx context.Context, run *AgentRun) (*AgentRun, bool, error) {
	if run.IdempotencyKey != "" {
		stored, err := r.runByIdempotencyKey(ctx, run.WorkItemID, run.RequestedBy, run.IdempotencyKey)
		if err != nil {
			return nil, false, err
		}
		if stored != nil {
			return stored, false, nil
		}
	}

	err := r.CreateRun(ctx, run)
	if err == nil {
		return run, true, nil
	}

	// A unique violation here can be the idempotency index or the one-live-run
	// index. Only the former has a row to return; if there is none, the error
	// stands as it came back (ErrAgentRunActive when the live-run rule fired).
	if run.IdempotencyKey != "" {
		stored, readErr := r.runByIdempotencyKey(ctx, run.WorkItemID, run.RequestedBy, run.IdempotencyKey)
		if readErr != nil {
			return nil, false, readErr
		}
		if stored != nil {
			return stored, false, nil
		}
	}
	return nil, false, err
}

func (r *AgentRepository) runByIdempotencyKey(ctx context.Context, workItemID WorkItemID, userID UserID, key string) (*AgentRun, error) {
	row := r.db.QueryRowContext(ctx, `SELECT `+agentRunColumns+`
	          FROM agent_runs
	          WHERE work_item_id = $1 AND requested_by = $2 AND idempotency_key = $3`,
		workItemID, userID, key)
	return oneOrNil(row, scanAgentRun)
}

// ClaimRun takes the oldest queued run requested by userID, in a project that
// user still belongs to, and leases it to runnerID.
//
// One statement, and the status predicate is repeated in the outer WHERE on
// purpose. Two runners evaluating the inner subquery together can pick the same
// row; the loser blocks on the row lock, re-checks `status = 'queued'`, matches
// nothing and returns no run. A handler-level read-then-write would let both
// claim a run to two machines.
//
// The membership condition is a safety net rather than the authorization check:
// requested_by already scopes the claim to this user, but a member removed from
// the project after dispatching must not have their machine run the task. Such a
// run stays queued; pruning it is the queue's job, not the claim's.
//
// Returns (nil, nil) when nothing is queued, which is what the long poll waits on.
func (r *AgentRepository) ClaimRun(ctx context.Context, runnerID AgentRunnerID, userID UserID, leaseUntil Timestamp) (*AgentRun, error) {
	row := r.db.QueryRowContext(ctx, `UPDATE agent_runs
	          SET status = 'claimed',
	              runner_id = $1,
	              lease_expires_at = $2,
	              started_at = COALESCE(started_at, $3)
	          WHERE id = (
	                  SELECT id FROM agent_runs
	                   WHERE status = 'queued'
	                     AND requested_by = $4
	                     AND project_id IN (SELECT project_id FROM project_members WHERE user_id = $4)
	                     -- A project-scoped machine sees only its own project's
	                     -- queue. The condition belongs inside the candidate
	                     -- selection: after LIMIT 1 it would let an older run from
	                     -- another project hide this runner's own work.
	                     AND EXISTS (
	                         SELECT 1 FROM agent_runners ar
	                          WHERE ar.id = $1
	                            AND (ar.project_id IS NULL OR ar.project_id = agent_runs.project_id)
	                     )
	                   ORDER BY created_at, id
	                   LIMIT 1
	                )
	            AND status = 'queued'
	            -- A stopped machine keeps its token but is owed no new work.
	            AND NOT EXISTS (SELECT 1 FROM agent_runners WHERE id = $1 AND paused_at IS NOT NULL)
	          RETURNING `+agentRunColumns,
		runnerID, leaseUntil, Now(), userID)

	return oneOrNil(row, scanAgentRun)
}

// AppendEvents records a batch of progress and extends the lease.
//
// The update both promotes a freshly claimed run to `running` and renews the
// lease, because a long tool call is not a dead runner: the batch is proof the
// worker is alive, so it is treated as a heartbeat too. A run the runner does
// not hold is refused rather than appended to — a stale worker must not invent
// progress on a run the server already reassigned.
//
// The assigned seq of the last event is returned; the client polls with that
// value as `afterSeq`.
func (r *AgentRepository) AppendEvents(ctx context.Context, runID AgentRunID, runnerID AgentRunnerID, leaseUntil Timestamp, events []AgentRunEvent) (int64, error) {
	if len(events) == 0 {
		return 0, nil
	}

	var lastSeq int64
	err := inTx(ctx, r.db, func(q Querier) error {
		// Placeholders ascend in text order: SQLite numbers them by appearance.
		result, err := q.ExecContext(ctx, `UPDATE agent_runs
		          SET status = 'running', lease_expires_at = $1
		          WHERE id = $2 AND runner_id = $3 AND status IN ('claimed', 'running')`,
			leaseUntil, runID, runnerID)
		if err != nil {
			return err
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if affected == 0 {
			return ErrAgentRunNotClaimed
		}

		if err := q.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq), 0) FROM agent_run_events WHERE run_id = $1`, runID).Scan(&lastSeq); err != nil {
			return err
		}

		for _, event := range events {
			lastSeq++
			createdAt := event.CreatedAt
			if createdAt.IsZero() {
				createdAt = Now()
			}
			if _, err := q.ExecContext(ctx, `INSERT INTO agent_run_events (run_id, seq, kind, message, payload, created_at)
			          VALUES ($1, $2, $3, $4, $5, $6)`,
				runID, lastSeq, event.Kind, event.Message, event.Payload, createdAt); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return lastSeq, nil
}

// HeartbeatRun renews the lease and tells the worker whether a cancel is waiting.
//
// The cancel flag is read here rather than pushed, which is why cancel is
// cooperative: the worker learns about it on its next beat and stops its own
// work. Killing a process on another machine is not a capability this server has.
func (r *AgentRepository) HeartbeatRun(ctx context.Context, runID AgentRunID, runnerID AgentRunnerID, leaseUntil Timestamp) (bool, *Timestamp, error) {
	var cancelRequested bool
	var lease sql.NullInt64

	err := r.db.QueryRowContext(ctx, `UPDATE agent_runs
	          SET lease_expires_at = $1
	          WHERE id = $2 AND runner_id = $3 AND status IN ('claimed', 'running')
	          RETURNING cancel_requested, lease_expires_at`,
		leaseUntil, runID, runnerID).Scan(&cancelRequested, &lease)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil, ErrAgentRunNotClaimed
	}
	if err != nil {
		return false, nil, err
	}
	return cancelRequested, timestampOrNil(lease), nil
}

// FinishRun records the terminal outcome and frees the one-live-run slot.
//
// It is idempotent by status: a worker whose response was lost retries the same
// finish and gets the stored run back instead of an error, because the work is
// already done and re-reporting it must not fail. It is not open to a worker
// that never held the run.
func (r *AgentRepository) FinishRun(ctx context.Context, runID AgentRunID, runnerID AgentRunnerID, status AgentRunStatus, summary, detail string) (*AgentRun, error) {
	switch status {
	case AgentRunSucceeded, AgentRunFailed, AgentRunCancelled:
	default:
		return nil, fmt.Errorf("agent run %s cannot finish as %q", runID, status)
	}

	row := r.db.QueryRowContext(ctx, `UPDATE agent_runs
	          SET status = $1, summary = $2, error = $3, finished_at = $4, lease_expires_at = NULL
	          WHERE id = $5 AND runner_id = $6 AND status IN ('claimed', 'running')
	          RETURNING `+agentRunColumns,
		status, summary, detail, Now(), runID, runnerID)

	run, err := oneOrNil(row, scanAgentRun)
	if err != nil {
		return nil, err
	}
	if run != nil {
		return run, nil
	}

	// No row was updated: either this runner already finished it (the retry
	// case) or it is not this runner's run at all.
	stored, err := r.GetRun(ctx, runID)
	if err != nil {
		return nil, err
	}
	if stored == nil {
		return nil, ErrAgentRunNotFound
	}
	if stored.RunnerID != nil && *stored.RunnerID == runnerID && stored.Status.IsTerminal() {
		return stored, nil
	}
	return nil, ErrAgentRunNotClaimed
}

// RequestCancel asks the runner to stop. The caller authorizes this: it must have
// read the run and confirmed the caller is a member of its project.
//
// A run that already finished is answered with itself — cancelling a done run is
// a no-op, not an error the app has to special-case.
func (r *AgentRepository) RequestCancel(ctx context.Context, runID AgentRunID) (*AgentRun, error) {
	row := r.db.QueryRowContext(ctx, `UPDATE agent_runs
	          SET cancel_requested = TRUE
	          WHERE id = $1 AND status IN ('queued', 'claimed', 'running')
	          RETURNING `+agentRunColumns, runID)

	run, err := oneOrNil(row, scanAgentRun)
	if err != nil {
		return nil, err
	}
	if run != nil {
		return run, nil
	}

	stored, err := r.GetRun(ctx, runID)
	if err != nil {
		return nil, err
	}
	if stored == nil {
		return nil, ErrAgentRunNotFound
	}
	return stored, nil
}

// ReapExpiredRuns fails every run whose lease has lapsed and returns the ids it
// failed.
//
// One statement rather than a select followed by updates: a worker that
// heartbeats between the read and the write would be failed by the update, and
// the run it is still working on would lose its slot. With the lease conditions
// inside the same UPDATE that clears the lease, only a run that is still stale at
// the moment of the write can be closed.
//
// A queued run has no lease and is never reaped: nobody has taken it yet, and the
// dispatch waiting there is the user's, not a dead worker's.
func (r *AgentRepository) ReapExpiredRuns(ctx context.Context, now Timestamp, detail string) ([]AgentRunID, error) {
	rows, err := r.db.QueryContext(ctx, `UPDATE agent_runs
	          SET finished_at = $1, status = 'failed', error = $2, lease_expires_at = NULL
	          WHERE status IN ('claimed', 'running')
	            AND lease_expires_at IS NOT NULL
	            AND lease_expires_at < $1
	          RETURNING id`, now, detail)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var ids []AgentRunID
	for rows.Next() {
		var id AgentRunID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// GetRun reads one run. It returns (nil, nil) when there is no such row, the
// contract the rest of this package's GetByID methods offer.
func (r *AgentRepository) GetRun(ctx context.Context, id AgentRunID) (*AgentRun, error) {
	row := r.db.QueryRowContext(ctx, `SELECT `+agentRunColumns+` FROM agent_runs WHERE id = $1`, id)
	return oneOrNil(row, scanAgentRun)
}

// ListRunsByWorkItem returns a work item's runs, newest first. The app uses it to
// recover the last run when it reopens — a run outliving the app is normal.
func (r *AgentRepository) ListRunsByWorkItem(ctx context.Context, workItemID WorkItemID, limit int) ([]*AgentRun, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+agentRunColumns+`
	          FROM agent_runs WHERE work_item_id = $1
	          ORDER BY created_at DESC, id DESC
	          LIMIT $2`, workItemID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return collectRows(rows, scanAgentRun)
}

// ListEventsAfter reads the progress log after a cursor, oldest first.
func (r *AgentRepository) ListEventsAfter(ctx context.Context, runID AgentRunID, afterSeq int64, limit int) ([]*AgentRunEvent, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT seq, kind, message, payload, created_at
	          FROM agent_run_events
	          WHERE run_id = $1 AND seq > $2
	          ORDER BY seq
	          LIMIT $3`, runID, afterSeq, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var events []*AgentRunEvent
	for rows.Next() {
		var event AgentRunEvent
		if err := rows.Scan(&event.Seq, &event.Kind, &event.Message, &event.Payload, &event.CreatedAt); err != nil {
			return nil, err
		}
		events = append(events, &event)
	}
	return events, rows.Err()
}

// RegisterRunner stores a paired machine. The caller mints the token, hashes it,
// and keeps the plaintext only long enough to show it once; this repository never
// sees it.
func (r *AgentRepository) RegisterRunner(ctx context.Context, runner *AgentRunner, tokenHash string) error {
	return insertRunner(ctx, r.db, runner, tokenHash)
}

// insertRunner is shared by registration and pairing so the two paths cannot
// disagree about the columns, the id hash, or the timestamps. The write names
// token_hash explicitly: agentRunnerColumns is the read projection, and that
// deliberately never carries the hash out of the table.
func insertRunner(ctx context.Context, q Querier, runner *AgentRunner, tokenHash string) error {
	if strings.TrimSpace(tokenHash) == "" {
		return errors.New("agent runner token hash is required")
	}
	runner.ID = orNewID(runner.ID, NewAgentRunnerID)
	if runner.CreatedAt.IsZero() {
		runner.CreatedAt = Now()
	}

	_, err := q.ExecContext(ctx, `INSERT INTO agent_runners
	          (id, user_id, project_id, name, token_hash, provider, model_label, last_seen_at, revoked_at, created_at, paused_at)
	          VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
		runner.ID, runner.UserID, nullableProjectID(runner.ProjectID), runner.Name, tokenHash, runner.Provider, runner.ModelLabel,
		runner.LastSeenAt, runner.RevokedAt, runner.CreatedAt, runner.PausedAt)
	return err
}

func nullableProjectID(id *ProjectID) any {
	if id == nil {
		return nil
	}
	return string(*id)
}

// RunnerByTokenHash resolves a bearer token to the machine that holds it. A
// revoked runner is not found, so revocation takes effect on the next request
// rather than when a token happens to expire.
func (r *AgentRepository) RunnerByTokenHash(ctx context.Context, tokenHash string) (*AgentRunner, error) {
	row := r.db.QueryRowContext(ctx, `SELECT `+agentRunnerColumns+`
	          FROM agent_runners
	          WHERE token_hash = $1 AND revoked_at IS NULL`, tokenHash)
	return oneOrNil(row, scanAgentRunner)
}

// ListRunners returns the caller's machines, revoked ones included, newest first.
func (r *AgentRepository) ListRunners(ctx context.Context, userID UserID) ([]*AgentRunner, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+agentRunnerColumns+`
	          FROM agent_runners WHERE user_id = $1
	          ORDER BY created_at DESC, id DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return collectRows(rows, scanAgentRunner)
}

// SetRunnerPaused stops or restarts a paired machine.
//
// Pausing is not revocation: the token stays valid and the same machine can be
// started again, which is what makes this a switch rather than a retirement.
// The claim query is what enforces the stop, so a paused machine that is still
// running `tt agent run` simply receives no work.
//
// A live run is flagged for cancel in the same transaction, because a stop that
// let the current task finish would report the agent as stopped while it was
// still editing files. Cancel is cooperative — the worker observes the flag on
// its next heartbeat — which is the most this server can do to a process it does
// not own.
func (r *AgentRepository) SetRunnerPaused(ctx context.Context, id AgentRunnerID, paused bool) (*AgentRunner, error) {
	var runner *AgentRunner
	err := inTx(ctx, r.db, func(q Querier) error {
		// Placeholders ascend in text order: SQLite numbers them by appearance.
		row := q.QueryRowContext(ctx, `UPDATE agent_runners
		          SET paused_at = CASE WHEN $1 THEN COALESCE(paused_at, $2) ELSE NULL END
		          WHERE id = $3 AND revoked_at IS NULL
		          RETURNING `+agentRunnerColumns,
			paused, Now(), id)
		updated, err := oneOrNil(row, scanAgentRunner)
		if err != nil {
			return err
		}
		if updated == nil {
			return ErrAgentRunnerNotFound
		}

		if paused {
			if _, err := q.ExecContext(ctx, `UPDATE agent_runs
			          SET cancel_requested = TRUE
			          WHERE runner_id = $1 AND status IN ('claimed', 'running')`, id); err != nil {
				return err
			}
		}
		runner = updated
		return nil
	})
	if err != nil {
		return nil, err
	}
	return runner, nil
}

// RevokeRunner stops a machine from claiming or reporting work.
//
// The update is scoped by user_id, so one account cannot revoke another's
// machine, and it keeps the first revocation date: the second call is a no-op
// rather than a new timestamp.
func (r *AgentRepository) RevokeRunner(ctx context.Context, id AgentRunnerID, userID UserID) error {
	return execOneRow(ctx, r.db, ErrAgentRunnerNotFound,
		`UPDATE agent_runners SET revoked_at = COALESCE(revoked_at, $1) WHERE id = $2 AND user_id = $3`,
		Now(), id, userID)
}

// TouchRunner records that a runner was seen. A runner removed between two
// requests is not an error here: the request that follows will fail its own
// authorization.
func (r *AgentRepository) TouchRunner(ctx context.Context, id AgentRunnerID) error {
	_, err := r.db.ExecContext(ctx, `UPDATE agent_runners SET last_seen_at = $1 WHERE id = $2`, Now(), id)
	return err
}

// agentRunAlreadyActive reports whether err is the partial index refusing a second
// live run. Both drivers name the constraint, so the check reads the driver's
// error rather than matching on message text where it can avoid it.
func agentRunAlreadyActive(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		// An empty name means the server did not report one; on this table a
		// unique violation can only be the one-live-run index or the id primary
		// key, and a minted UUID colliding is not a case worth a separate error.
		return pgErr.Code == "23505" &&
			(pgErr.ConstraintName == "" || pgErr.ConstraintName == "idx_agent_runs_one_live")
	}

	// go-sqlite3 returns its Error by value, not by pointer.
	var liteErr sqlite3.Error
	if errors.As(err, &liteErr) {
		return liteErr.ExtendedCode == sqlite3.ErrConstraintUnique &&
			strings.Contains(liteErr.Error(), "work_item_id")
	}
	return false
}
