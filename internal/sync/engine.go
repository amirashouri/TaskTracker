// Package sync applies the client's queued changes and serves the change feed
// it pulls from. It routes opaque payloads and never invents domain rules.
package sync

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
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
	db *sql.DB
}

func NewEngine(db *sql.DB) *Engine {
	return &Engine{db: db}
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
	result := entities.PushResult{EntryID: change.ID}
	// Without an id the log row could not point back at the stored entity.
	if change.EntityID == "" {
		result.Status = StatusRejected
		result.Message = "entityId is required"
		return result
	}
	switch change.Entity {
	case entities.OutboxEntityProject, entities.OutboxEntityWorkItem,
		entities.OutboxEntitySprint, entities.OutboxEntityComment:
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
	if projectID != "" {
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
			result.Status = StatusRetryLater
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

// Pull returns the changes for one entity after the cursor, oldest first,
// scoped to the projects the caller is a member of. A project's own delete row
// is the exception: it is delivered to every authenticated user, so a client
// that has been removed from a project — or whose project was deleted — still
// retires it locally instead of keeping it forever.
func (e *Engine) Pull(ctx context.Context, userID string, entity entities.OutboxEntity, cursor string, limit int) (*entities.PullPage, error) {
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

	rows, err := e.db.QueryContext(ctx, `
		SELECT id, entity, entity_id, operation, payload, version
		  FROM sync_log
		 WHERE entity = $1 AND id > $2
		   AND ( project_id IN (SELECT project_id FROM project_members WHERE user_id = $3)
		         OR ($1 = 'project' AND operation = 'delete') )
		 ORDER BY id
		 LIMIT $4`,
		string(entity), after, userID, limit+1)
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
	return page, nil
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
