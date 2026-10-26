// Package sync applies the client's queued changes and serves the change feed
// it pulls from. It routes opaque payloads and never invents domain rules.
package sync

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/amirashouri/tasktracker-server/internal/entities"
)

const (
	defaultPullLimit = 200
	maxPullLimit     = 500
)

// Push verdicts understood by the client.
const (
	StatusAccepted   = "accepted"
	StatusConflict   = "conflict"
	StatusRejected   = "rejected"
	StatusRetryLater = "retry_later"
)

type Engine struct {
	db             *sql.DB
	attachmentRoot string
}

// NewEngine builds the sync engine. attachmentRoot is where uploaded blobs are
// stored (see internal/attachments); it is threaded here so an attachment delete
// can remove its bytes in the same transaction as the row.
func NewEngine(db *sql.DB, attachmentRoot string) *Engine {
	return &Engine{db: db, attachmentRoot: attachmentRoot}
}

// versionConflict reports that the row moved on since the client read it.
type versionConflict struct {
	current string
}

func (c *versionConflict) Error() string { return "version conflict" }

// errNoProject marks an upsert that names no project, so it cannot be placed
// in a change feed or authorized.
var errNoProject = errors.New("change does not name a project")

// Push applies each change in order and returns one verdict per change. A
// failure on one change never stops the rest.
func (e *Engine) Push(ctx context.Context, userID string, changes []entities.PushChange) ([]entities.PushResult, error) {
	results := make([]entities.PushResult, 0, len(changes))
	for _, change := range changes {
		results = append(results, e.apply(ctx, userID, change))
	}
	return results, nil
}

func (e *Engine) apply(ctx context.Context, userID string, change entities.PushChange) entities.PushResult {
	return e.applyChange(ctx, userID, change, true)
}

// ApplyAdminProject applies a project change authored by an admin and records it
// in the change feed, so the project's members pull it exactly as they would a
// change one of their own clients pushed.
//
// It is not the push path: an admin may edit a project they are not a member of,
// and a push is authorized by membership. The caller is authorized by the admin
// route instead, so the membership check is the one thing this skips. Everything
// else — the version compare-and-swap, the write and its log row in one
// transaction — is shared with apply, because a change that skipped the log row
// would be invisible to every client.
func (e *Engine) ApplyAdminProject(ctx context.Context, actorID string, change entities.PushChange) entities.PushResult {
	if change.Entity != entities.OutboxEntityProject {
		return entities.PushResult{
			EntryID: change.ID,
			Status:  StatusRejected,
			Message: fmt.Sprintf("the admin API writes projects, not %q", change.Entity),
		}
	}
	return e.applyChange(ctx, actorID, change, false)
}

// applyChange is the body of apply. checkMembership is false only for a change
// already authorized by an admin-only route.
func (e *Engine) applyChange(ctx context.Context, userID string, change entities.PushChange, checkMembership bool) entities.PushResult {
	result := entities.PushResult{EntryID: change.ID}
	// Without an id the log row could not point back at the stored entity.
	if change.EntityID == "" {
		result.Status = StatusRejected
		result.Message = "entityId is required"
		return result
	}
	switch change.Entity {
	case entities.OutboxEntityProject, entities.OutboxEntityWorkItem,
		entities.OutboxEntitySprint, entities.OutboxEntityComment, entities.OutboxEntityAttachment:
	default:
		result.Status = StatusRejected
		result.Message = fmt.Sprintf("unknown entity %q", change.Entity)
		return result
	}

	// One transaction per change: the entity write and its log row commit
	// together, so a crash between them cannot strand a change with nothing
	// queued. Each change is independent — one failure never rolls back another.
	tx, err := e.db.BeginTx(ctx, nil)
	if err != nil {
		result.Status = StatusRetryLater
		result.Message = err.Error()
		return result
	}
	defer tx.Rollback()

	// Authorize before writing: a change is applied only if the caller may see
	// the project it belongs to.
	projectID, err := resolveProject(ctx, tx, change)
	if err != nil {
		result.Status = StatusRetryLater
		result.Message = err.Error()
		return result
	}
	if projectID == "" && change.Operation != entities.OutboxOperationDelete {
		result.Status = StatusRejected
		result.Message = errNoProject.Error()
		return result
	}
	if projectID != "" && checkMembership {
		allowed, err := canWrite(ctx, tx, userID, change, projectID)
		if err != nil {
			result.Status = StatusRetryLater
			result.Message = err.Error()
			return result
		}
		if !allowed {
			result.Status = StatusRejected
			result.Message = "not a member of this project"
			return result
		}
	}

	// Repositories bound to the transaction, so their writes join it.
	var version string
	switch change.Entity {
	case entities.OutboxEntityProject:
		version, err = applyProject(ctx, entities.NewProjectRepository(tx), change)
	case entities.OutboxEntityWorkItem:
		version, err = applyWorkItem(ctx, entities.NewWorkItemRepository(tx), change)
	case entities.OutboxEntitySprint:
		version, err = applySprint(ctx, entities.NewSprintRepository(tx), change)
	case entities.OutboxEntityComment:
		version, err = applyComment(ctx, entities.NewCommentRepository(tx), change)
	case entities.OutboxEntityAttachment:
		version, err = applyAttachment(ctx, e.attachmentRoot, entities.NewAttachmentRepository(tx), change)
	}

	if err != nil {
		var conflict *versionConflict
		switch {
		case errors.As(err, &conflict):
			result.Status = StatusConflict
			result.Version = conflict.current
			result.Message = "version mismatch"
		case errors.Is(err, entities.ErrVersionConflict):
			// The row moved between the read and the write. Re-read it so the
			// verdict still tells the client what the server now holds.
			result.Status = StatusConflict
			result.Message = "version mismatch"
			if current, err := currentVersion(ctx, tx, change); err == nil {
				result.Version = current
			}
		default:
			// A write the row's own values cannot satisfy is refused for good, and
			// saying so spares the client a retry budget it cannot spend. The
			// log-row and commit failures below stay `retry_later` even on a
			// permanent database error: a failure there is the server's own, and
			// `rejected` would dead-letter client work over it.
			if entities.PermanentWriteError(err) {
				result.Status = StatusRejected
			} else {
				result.Status = StatusRetryLater
			}
			result.Message = err.Error()
		}
		return result
	}

	if err := appendLog(ctx, tx, projectID, change, userID, version); err != nil {
		result.Status = StatusRetryLater
		result.Message = err.Error()
		return result
	}

	if err := tx.Commit(); err != nil {
		result.Status = StatusRetryLater
		result.Message = err.Error()
		return result
	}

	result.Status = StatusAccepted
	result.Version = version
	return result
}

// resolveProject reports which project a change belongs to, so it can be
// authorized and recorded on the log row. An empty id means the change names
// no project: for a delete that is a no-op, for an upsert it is rejected.
func resolveProject(ctx context.Context, q entities.Querier, change entities.PushChange) (string, error) {
	switch change.Entity {
	case entities.OutboxEntityProject:
		return change.EntityID, nil

	case entities.OutboxEntityWorkItem:
		if change.Operation == entities.OutboxOperationDelete {
			return projectOfWorkItem(ctx, q, change.EntityID)
		}
		item, err := entities.WorkItemFromJSON(change.Payload)
		if err != nil {
			return "", fmt.Errorf("decode work item: %w", err)
		}
		return string(item.ProjectID), nil

	case entities.OutboxEntitySprint:
		if change.Operation == entities.OutboxOperationDelete {
			return projectOfSprint(ctx, q, change.EntityID)
		}
		sprint, err := entities.SprintFromJSON(change.Payload)
		if err != nil {
			return "", fmt.Errorf("decode sprint: %w", err)
		}
		return string(sprint.ProjectID), nil

	case entities.OutboxEntityComment:
		// A comment names a work item, not a project; the work item names the
		// project. On a delete the comment must be read before it is removed.
		var workItemID string
		if change.Operation == entities.OutboxOperationDelete {
			comment, err := entities.NewCommentRepository(q).GetByID(ctx, entities.CommentID(change.EntityID))
			if err != nil {
				return "", err
			}
			if comment == nil {
				return "", nil
			}
			workItemID = string(comment.WorkItemID)
		} else {
			comment, err := entities.CommentFromJSON(change.Payload)
			if err != nil {
				return "", fmt.Errorf("decode comment: %w", err)
			}
			workItemID = string(comment.WorkItemID)
		}
		if workItemID == "" {
			return "", nil
		}
		return projectOfWorkItem(ctx, q, workItemID)

	case entities.OutboxEntityAttachment:
		// An attachment names a work item, not a project; the work item names the
		// project. On a delete the row must be read before it is removed.
		var workItemID string
		if change.Operation == entities.OutboxOperationDelete {
			att, err := entities.NewAttachmentRepository(q).GetByID(ctx, entities.AttachmentID(change.EntityID))
			if err != nil {
				return "", err
			}
			if att == nil {
				return "", nil
			}
			workItemID = string(att.WorkItemID)
		} else {
			att, err := entities.AttachmentFromJSON(change.Payload)
			if err != nil {
				return "", fmt.Errorf("decode attachment: %w", err)
			}
			workItemID = string(att.WorkItemID)
		}
		if workItemID == "" {
			return "", nil
		}
		return projectOfWorkItem(ctx, q, workItemID)
	}
	return "", fmt.Errorf("unknown entity %q", change.Entity)
}

func projectOfWorkItem(ctx context.Context, q entities.Querier, workItemID string) (string, error) {
	item, err := entities.NewWorkItemRepository(q).GetByID(ctx, entities.WorkItemID(workItemID))
	if err != nil || item == nil {
		return "", err
	}
	return string(item.ProjectID), nil
}

func projectOfSprint(ctx context.Context, q entities.Querier, sprintID string) (string, error) {
	sprint, err := entities.NewSprintRepository(q).GetByID(ctx, entities.SprintID(sprintID))
	if err != nil || sprint == nil {
		return "", err
	}
	return string(sprint.ProjectID), nil
}

// canWrite reports whether the caller may write this change. A project that
// does not exist yet has no membership rows, so the payload's member list is
// what authorizes its creation — which keeps creation open to anyone who names
// themselves a member without letting anyone join a project they cannot see.
func canWrite(ctx context.Context, q entities.Querier, userID string, change entities.PushChange, projectID string) (bool, error) {
	if change.Entity == entities.OutboxEntityProject && change.Operation != entities.OutboxOperationDelete {
		exists, err := projectExists(ctx, q, projectID)
		if err != nil {
			return false, err
		}
		if !exists {
			project, err := entities.ProjectFromJSON(change.Payload)
			if err != nil {
				return false, fmt.Errorf("decode project: %w", err)
			}
			return containsUser(project.MemberIDs, userID), nil
		}
	}
	return isMember(ctx, q, userID, projectID)
}

func projectExists(ctx context.Context, q entities.Querier, projectID string) (bool, error) {
	var count int
	if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM projects WHERE id = $1`, projectID).Scan(&count); err != nil {
		return false, err
	}
	return count > 0, nil
}

func isMember(ctx context.Context, q entities.Querier, userID, projectID string) (bool, error) {
	var count int
	err := q.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM project_members WHERE project_id = $1 AND user_id = $2`,
		projectID, userID).Scan(&count)
	return count > 0, err
}

// IsMember reports whether userID belongs to projectID. It is exported so the
// attachment file API can scope blob access to the same membership the change
// feed enforces — one query, so the two checks cannot diverge.
func (e *Engine) IsMember(ctx context.Context, userID, projectID string) (bool, error) {
	return isMember(ctx, e.db, userID, projectID)
}

func containsUser(ids []entities.UserID, userID string) bool {
	for _, id := range ids {
		if string(id) == userID {
			return true
		}
	}
	return false
}

// currentVersion re-reads an entity's version, so a conflict verdict can tell
// the client what the server holds and it can re-pull.
func currentVersion(ctx context.Context, q entities.Querier, change entities.PushChange) (string, error) {
	switch change.Entity {
	case entities.OutboxEntityProject:
		row, err := entities.NewProjectRepository(q).GetByID(ctx, entities.ProjectID(change.EntityID))
		if err != nil || row == nil {
			return "", err
		}
		return row.Version, nil
	case entities.OutboxEntityWorkItem:
		row, err := entities.NewWorkItemRepository(q).GetByID(ctx, entities.WorkItemID(change.EntityID))
		if err != nil || row == nil {
			return "", err
		}
		return row.Version, nil
	case entities.OutboxEntitySprint:
		row, err := entities.NewSprintRepository(q).GetByID(ctx, entities.SprintID(change.EntityID))
		if err != nil || row == nil {
			return "", err
		}
		return row.Version, nil
	case entities.OutboxEntityComment:
		row, err := entities.NewCommentRepository(q).GetByID(ctx, entities.CommentID(change.EntityID))
		if err != nil || row == nil {
			return "", err
		}
		return row.Version, nil
	case entities.OutboxEntityAttachment:
		row, err := entities.NewAttachmentRepository(q).GetByID(ctx, entities.AttachmentID(change.EntityID))
		if err != nil || row == nil {
			return "", err
		}
		return row.Version, nil
	}
	return "", nil
}

func applyProject(ctx context.Context, projects *entities.ProjectRepository, change entities.PushChange) (string, error) {
	if change.Operation == entities.OutboxOperationDelete {
		return "", projects.Delete(ctx, entities.ProjectID(change.EntityID))
	}

	project, err := entities.ProjectFromJSON(change.Payload)
	if err != nil {
		return "", fmt.Errorf("decode project: %w", err)
	}
	existing, err := projects.GetByID(ctx, entities.ProjectID(change.EntityID))
	if err != nil {
		return "", err
	}
	if existing == nil {
		project.ID = entities.ProjectID(change.EntityID)
		if err := projects.Create(ctx, project); err != nil {
			return "", err
		}
		return project.Version, nil
	}
	if change.Version != "" && existing.Version != change.Version {
		return "", &versionConflict{current: existing.Version}
	}
	project.ID = existing.ID
	project.CreatedAt = existing.CreatedAt
	// Update's compare-and-swap must run against the row just read, not the
	// payload's version — which the client may have omitted.
	project.Version = existing.Version
	if err := projects.Update(ctx, project); err != nil {
		return "", err
	}
	return project.Version, nil
}

func applyWorkItem(ctx context.Context, workItems *entities.WorkItemRepository, change entities.PushChange) (string, error) {
	if change.Operation == entities.OutboxOperationDelete {
		return "", workItems.Delete(ctx, entities.WorkItemID(change.EntityID))
	}

	workItem, err := entities.WorkItemFromJSON(change.Payload)
	if err != nil {
		return "", fmt.Errorf("decode work item: %w", err)
	}
	existing, err := workItems.GetByID(ctx, entities.WorkItemID(change.EntityID))
	if err != nil {
		return "", err
	}
	if existing == nil {
		workItem.ID = entities.WorkItemID(change.EntityID)
		if err := workItems.Create(ctx, workItem); err != nil {
			return "", err
		}
		return workItem.Version, nil
	}
	if change.Version != "" && existing.Version != change.Version {
		return "", &versionConflict{current: existing.Version}
	}
	workItem.ID = existing.ID
	workItem.CreatedAt = existing.CreatedAt
	workItem.Version = existing.Version
	if err := workItems.Update(ctx, workItem); err != nil {
		return "", err
	}
	return workItem.Version, nil
}

func applySprint(ctx context.Context, sprints *entities.SprintRepository, change entities.PushChange) (string, error) {
	if change.Operation == entities.OutboxOperationDelete {
		return "", sprints.Delete(ctx, entities.SprintID(change.EntityID))
	}

	sprint, err := entities.SprintFromJSON(change.Payload)
	if err != nil {
		return "", fmt.Errorf("decode sprint: %w", err)
	}
	existing, err := sprints.GetByID(ctx, entities.SprintID(change.EntityID))
	if err != nil {
		return "", err
	}
	if existing == nil {
		sprint.ID = entities.SprintID(change.EntityID)
		if err := sprints.Create(ctx, sprint); err != nil {
			return "", err
		}
		return sprint.Version, nil
	}
	if change.Version != "" && existing.Version != change.Version {
		return "", &versionConflict{current: existing.Version}
	}
	sprint.ID = existing.ID
	sprint.CreatedAt = existing.CreatedAt
	sprint.Version = existing.Version
	if err := sprints.Update(ctx, sprint); err != nil {
		return "", err
	}
	return sprint.Version, nil
}

func applyComment(ctx context.Context, comments *entities.CommentRepository, change entities.PushChange) (string, error) {
	if change.Operation == entities.OutboxOperationDelete {
		return "", comments.Delete(ctx, entities.CommentID(change.EntityID))
	}

	comment, err := entities.CommentFromJSON(change.Payload)
	if err != nil {
		return "", fmt.Errorf("decode comment: %w", err)
	}
	existing, err := comments.GetByID(ctx, entities.CommentID(change.EntityID))
	if err != nil {
		return "", err
	}
	if existing == nil {
		comment.ID = entities.CommentID(change.EntityID)
		if err := comments.Create(ctx, comment); err != nil {
			return "", err
		}
		return comment.Version, nil
	}
	if change.Version != "" && existing.Version != change.Version {
		return "", &versionConflict{current: existing.Version}
	}
	comment.ID = existing.ID
	comment.CreatedAt = existing.CreatedAt
	comment.Version = existing.Version
	if err := comments.Update(ctx, comment); err != nil {
		return "", err
	}
	return comment.Version, nil
}

// applyAttachment mirrors applyComment. On a delete it also removes the on-disk
// blob, because the row's removal is the only signal the blob store gets — a
// failed file delete is best-effort and must not fail the metadata delete.
func applyAttachment(ctx context.Context, attachmentRoot string, attachments *entities.AttachmentRepository, change entities.PushChange) (string, error) {
	if change.Operation == entities.OutboxOperationDelete {
		removeAttachmentBlob(attachmentRoot, change.EntityID)
		return "", attachments.Delete(ctx, entities.AttachmentID(change.EntityID))
	}

	attachment, err := entities.AttachmentFromJSON(change.Payload)
	if err != nil {
		return "", fmt.Errorf("decode attachment: %w", err)
	}
	existing, err := attachments.GetByID(ctx, entities.AttachmentID(change.EntityID))
	if err != nil {
		return "", err
	}
	if existing == nil {
		attachment.ID = entities.AttachmentID(change.EntityID)
		if err := attachments.Create(ctx, attachment); err != nil {
			return "", err
		}
		return attachment.Version, nil
	}
	if change.Version != "" && existing.Version != change.Version {
		return "", &versionConflict{current: existing.Version}
	}
	attachment.ID = existing.ID
	attachment.CreatedAt = existing.CreatedAt
	attachment.Version = existing.Version
	if err := attachments.Update(ctx, attachment); err != nil {
		return "", err
	}
	return attachment.Version, nil
}

// removeAttachmentBlob deletes an attachment's on-disk bytes, best-effort. The
// blob store is keyed flat by attachment id under attachmentRoot; a missing file
// (already removed, or never uploaded) is not worth failing the metadata delete.
func removeAttachmentBlob(root, attachmentID string) {
	if root == "" {
		return
	}
	_ = os.Remove(filepath.Join(root, attachmentID+".blob"))
}

// Scope is one change feed: the entities that travel on it.
//
// A scope is **not** an entity type, and this type exists because the feed used to be
// filtered to exactly one entity. `workItems` carries work items, the sprints they belong
// to and the comments on them — the client has always read that scope that way — so a
// sprint's log row was written, committed and then served to nobody: a second device pulled
// every card and no sprint, forever, because the row was behind its cursor by the time it
// learned to look. The client is the fixed point here; the feed is what was wrong.
//
// `projects` and `attachments` are single-entity feeds and stay that way.
//
// The values are the singular wire spellings the `sync_log` stores (`entities.OutboxEntity*`),
// not the plural path segments the client calls.
type Scope struct {
	entities []entities.OutboxEntity
}

// The three feeds the client pulls, named after its own `SyncScope` enum. There is no
// `sprints` route on purpose: a sprint reaches a client on `workItems`, and a feed of its
// own would be a second copy of every sprint to every board watcher.
var (
	ScopeProjects = Scope{entities: []entities.OutboxEntity{
		entities.OutboxEntityProject,
	}}
	ScopeWorkItems = Scope{entities: []entities.OutboxEntity{
		entities.OutboxEntityWorkItem,
		entities.OutboxEntitySprint,
		entities.OutboxEntityComment,
	}}
	ScopeAttachments = Scope{entities: []entities.OutboxEntity{
		entities.OutboxEntityAttachment,
	}}
)

// entityPlaceholders renders one `$n` per entity of the scope, starting at start, and
// returns the arguments that pair with them. This is what keeps the two queries below
// dialect-neutral: Postgres and SQLite both take an ordered `IN` list, and neither can bind
// a slice as a single parameter.
func (s Scope) entityPlaceholders(start int) (string, []any) {
	marks := make([]string, len(s.entities))
	args := make([]any, len(s.entities))
	for i, entity := range s.entities {
		marks[i] = "$" + strconv.Itoa(start+i)
		args[i] = string(entity)
	}
	return strings.Join(marks, ", "), args
}

// Pull returns the changes for one scope after the cursor, oldest first,
// scoped to the projects the caller is a member of. A project's own delete row
// is the exception: it is delivered to every authenticated user, so a client
// that has been removed from a project — or whose project was deleted — still
// retires it locally instead of keeping it forever.
func (e *Engine) Pull(ctx context.Context, userID string, scope Scope, cursor string, limit int) (*entities.PullPage, error) {
	after, err := parseCursor(cursor)
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = defaultPullLimit
	}
	if limit > maxPullLimit {
		limit = maxPullLimit
	}

	// The entity list first, then the cursor, the caller and the page size — the
	// placeholders are numbered off the scope's width rather than spelled out.
	in, args := scope.entityPlaceholders(1)
	cursorAt := len(args) + 1
	// The membership predicate and the `LIMIT` are positional too, so the whole query is
	// built once instead of being concatenated from two halves that could drift.
	query := fmt.Sprintf(`
		SELECT id, entity, entity_id, operation, payload, version
		  FROM sync_log
		 WHERE entity IN (%s) AND id > $%d
		   AND ( project_id IN (SELECT project_id FROM project_members WHERE user_id = $%d)
		         OR (entity = 'project' AND operation = 'delete') )
		 ORDER BY id
		 LIMIT $%d`, in, cursorAt, cursorAt+1, cursorAt+2)

	rows, err := e.db.QueryContext(ctx, query, append(args, after, userID, limit+1)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	page := &entities.PullPage{Changes: []entities.RemoteChange{}}
	var lastID int64
	for rows.Next() {
		var (
			id        int64
			rowEntity string
			entityID  string
			operation string
			payload   []byte
			version   string
		)
		if err := rows.Scan(&id, &rowEntity, &entityID, &operation, &payload, &version); err != nil {
			return nil, err
		}
		if len(page.Changes) == limit {
			page.HasMore = true
			break
		}
		page.Changes = append(page.Changes, entities.RemoteChange{
			Entity:    entities.OutboxEntity(rowEntity),
			EntityID:  entityID,
			Operation: entities.OutboxOperation(operation),
			Payload:   json.RawMessage(payload),
			Version:   version,
		})
		lastID = id
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if lastID > 0 {
		page.NextCursor = strconv.FormatInt(lastID, 10)
	}

	// A cursor that points before the oldest retained row means rows were
	// pruned under it: the client has missed changes and must start over.
	oldest, err := e.oldestCursor(ctx, scope)
	if err != nil {
		return nil, err
	}
	page.ResyncRequired = after > 0 && (oldest == 0 || after < oldest-1)

	return page, nil
}

// oldestCursor is the smallest id still retained for a scope, or 0 when the
// feed is empty. The (entity, created_at, id) index serves this ordering.
//
// It spans every entity of the scope on purpose: a pruned sprint under the cursor is just as
// lost to a client as a pruned work item, and a check that only looked at work items would
// report a clean sync for a gap it never noticed.
func (e *Engine) oldestCursor(ctx context.Context, scope Scope) (int64, error) {
	in, args := scope.entityPlaceholders(1)
	var oldest int64
	err := e.db.QueryRowContext(ctx,
		fmt.Sprintf(`SELECT id FROM sync_log WHERE entity IN (%s) ORDER BY created_at, id LIMIT 1`, in),
		args...).Scan(&oldest)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return oldest, err
}

// Prune deletes change-feed rows older than before and returns how many went.
// A pruned row is gone for good, which is why Pull reports ResyncRequired to a
// client whose cursor is older than what survived.
func (e *Engine) Prune(ctx context.Context, before time.Time) (int64, error) {
	result, err := e.db.ExecContext(ctx, `DELETE FROM sync_log WHERE created_at < $1`, before.UnixMilli())
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// appendLog writes the change feed row. It takes the caller's transaction so
// the entity write and this row commit together.
func appendLog(ctx context.Context, q entities.Querier, projectID string, change entities.PushChange, userID, version string) error {
	_, err := q.ExecContext(ctx, `
		INSERT INTO sync_log (entity, entity_id, operation, payload, version, user_id, project_id, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		string(change.Entity),
		change.EntityID,
		string(change.Operation),
		string(change.Payload),
		version,
		nullableString(userID),
		nullableString(projectID),
		time.Now().UnixMilli())
	return err
}

func parseCursor(cursor string) (int64, error) {
	if cursor == "" {
		return 0, nil
	}
	value, err := strconv.ParseInt(cursor, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid cursor %q", cursor)
	}
	return value, nil
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}
