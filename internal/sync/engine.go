// Package sync applies the client's queued changes and serves the change feed
// it pulls from. It routes opaque payloads and never invents domain rules.
package sync

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
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

// invalidProjectMetadata reports a project whose own payload values the row
// cannot hold, such as a repository URL that is not one. Re-sending it cannot
// help, so the change is rejected rather than retried: a permanent refusal that
// is answered `retry_later` spends the client's retry budget forever.
type invalidProjectMetadata struct{ err error }

func (e *invalidProjectMetadata) Error() string { return e.err.Error() }

func (e *invalidProjectMetadata) Unwrap() error { return e.err }

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
		entities.OutboxEntitySprint, entities.OutboxEntitySprintMember,
		entities.OutboxEntityComment, entities.OutboxEntityAttachment,
		entities.OutboxEntityEpic, entities.OutboxEntityNotification:
	default:
		result.Status = StatusRejected
		result.Message = fmt.Sprintf("unknown entity %q", change.Entity)
		return result
	}

	// A notification names no project and is scoped to its recipient rather than
	// to a project's membership, so it takes its own authorization path.
	if change.Entity == entities.OutboxEntityNotification {
		return e.applyNotificationChange(ctx, userID, change)
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
	// every project it belongs to. Most entities name one; an epic names its
	// whole project set, and the caller must be a member of all of it.
	projectIDs, err := resolveProjects(ctx, tx, change)
	if err != nil {
		result.Status = StatusRetryLater
		result.Message = err.Error()
		return result
	}
	if len(projectIDs) == 0 && change.Operation != entities.OutboxOperationDelete {
		result.Status = StatusRejected
		result.Message = errNoProject.Error()
		return result
	}
	if len(projectIDs) > 0 && checkMembership {
		allowed, err := canWrite(ctx, tx, userID, change, projectIDs)
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

	// What generation needs to know about the row *before* this write. Read here,
	// inside the same transaction, because after the write a changed assignee and
	// an unchanged one look identical.
	snapshot := notificationSnapshotFor(ctx, tx, change)

	// Repositories bound to the transaction, so their writes join it.
	var version string
	switch change.Entity {
	case entities.OutboxEntityProject:
		version, err = applyProject(ctx, entities.NewProjectRepository(tx), change)
	case entities.OutboxEntityWorkItem:
		version, err = applyWorkItem(ctx, entities.NewWorkItemRepository(tx), change)
	case entities.OutboxEntitySprint:
		version, err = applySprint(ctx, entities.NewSprintRepository(tx), change)
	case entities.OutboxEntitySprintMember:
		version, err = applySprintMember(ctx, entities.NewSprintMemberRepository(tx), change)
	case entities.OutboxEntityComment:
		version, err = applyComment(ctx, entities.NewCommentRepository(tx), change)
	case entities.OutboxEntityAttachment:
		version, err = applyAttachment(ctx, e.attachmentRoot, entities.NewAttachmentRepository(tx), change)
	case entities.OutboxEntityEpic:
		version, err = applyEpic(ctx, entities.NewEpicRepository(tx), change)
	}

	if err != nil {
		var conflict *versionConflict
		var metadata *invalidProjectMetadata
		var overlap *entities.SprintWindowOverlapError
		var unlinked *entities.EpicLinkError
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
		case errors.As(err, &metadata):
			// A value the project's own columns cannot hold. The message names the
			// field, and `rejected` is what stops the client resending it.
			result.Status = StatusRejected
			result.Message = metadata.Error()
		case errors.As(err, &unlinked):
			// The epic exists and does not span the item's project, so no retry
			// can make the link valid. The message names the epic in the way.
			result.Status = StatusRejected
			result.Message = unlinked.Error()
		case errors.As(err, &overlap):
			// The window collides with a sibling's row, so re-sending this
			// payload cannot clear it: `rejected` is what stops the client
			// retrying, and the message names the sprint in the way.
			result.Status = StatusRejected
			result.Message = overlap.Error()
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

	// The inbox row and its feed entry share this transaction. A failure here is
	// logged and swallowed: a derived notification must never fail the edit that
	// caused it.
	if n := notificationForChange(ctx, tx, userID, change, snapshot); n != nil {
		if err := writeGeneratedNotification(ctx, tx, userID, n); err != nil {
			slog.Warn("notification not written", "kind", n.Kind, "recipient", n.UserID, "error", err)
		}
	}

	if err := appendLog(ctx, tx, projectIDs, change, userID, version); err != nil {
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

// resolveProjects reports which projects a change belongs to, so it can be
// authorized and recorded as visible to each one. Most entities name exactly
// one project; an epic names its whole set, which is what lets one log row reach
// every linked project's members. An empty set means the change names no
// project: for a delete that is a no-op, for an upsert it is rejected.
func resolveProjects(ctx context.Context, q entities.Querier, change entities.PushChange) ([]string, error) {
	switch change.Entity {
	case entities.OutboxEntityProject:
		return projectSet(change.EntityID), nil

	case entities.OutboxEntityWorkItem:
		if change.Operation == entities.OutboxOperationDelete {
			return oneProject(projectOfWorkItem(ctx, q, change.EntityID))
		}
		item, err := entities.WorkItemFromJSON(change.Payload)
		if err != nil {
			return nil, fmt.Errorf("decode work item: %w", err)
		}
		return projectSet(string(item.ProjectID)), nil

	case entities.OutboxEntitySprint:
		if change.Operation == entities.OutboxOperationDelete {
			return oneProject(projectOfSprint(ctx, q, change.EntityID))
		}
		sprint, err := entities.SprintFromJSON(change.Payload)
		if err != nil {
			return nil, fmt.Errorf("decode sprint: %w", err)
		}
		return projectSet(string(sprint.ProjectID)), nil

	case entities.OutboxEntitySprintMember:
		if change.Operation == entities.OutboxOperationDelete {
			return oneProject(projectOfSprintMember(ctx, q, change.EntityID))
		}
		member, err := entities.SprintMemberFromJSON(change.Payload)
		if err != nil {
			return nil, fmt.Errorf("decode sprint member: %w", err)
		}
		return projectSet(string(member.ProjectID)), nil

	case entities.OutboxEntityComment:
		// A comment names a work item, not a project; the work item names the
		// project. On a delete the comment must be read before it is removed.
		var workItemID string
		if change.Operation == entities.OutboxOperationDelete {
			comment, err := entities.NewCommentRepository(q).GetByID(ctx, entities.CommentID(change.EntityID))
			if err != nil {
				return nil, err
			}
			if comment == nil {
				return nil, nil
			}
			workItemID = string(comment.WorkItemID)
		} else {
			comment, err := entities.CommentFromJSON(change.Payload)
			if err != nil {
				return nil, fmt.Errorf("decode comment: %w", err)
			}
			workItemID = string(comment.WorkItemID)
		}
		if workItemID == "" {
			return nil, nil
		}
		return oneProject(projectOfWorkItem(ctx, q, workItemID))

	case entities.OutboxEntityAttachment:
		// An attachment names a work item, not a project; the work item names the
		// project. On a delete the row must be read before it is removed.
		var workItemID string
		if change.Operation == entities.OutboxOperationDelete {
			att, err := entities.NewAttachmentRepository(q).GetByID(ctx, entities.AttachmentID(change.EntityID))
			if err != nil {
				return nil, err
			}
			if att == nil {
				return nil, nil
			}
			workItemID = string(att.WorkItemID)
		} else {
			att, err := entities.AttachmentFromJSON(change.Payload)
			if err != nil {
				return nil, fmt.Errorf("decode attachment: %w", err)
			}
			workItemID = string(att.WorkItemID)
		}
		if workItemID == "" {
			return nil, nil
		}
		return oneProject(projectOfWorkItem(ctx, q, workItemID))

	case entities.OutboxEntityEpic:
		if change.Operation == entities.OutboxOperationDelete {
			epic, err := entities.NewEpicRepository(q).GetByID(ctx, entities.EpicID(change.EntityID))
			if err != nil || epic == nil {
				return nil, err
			}
			return projectIDStrings(epic.ProjectIDSet()), nil
		}
		epic, err := entities.EpicFromJSON(change.Payload)
		if err != nil {
			return nil, fmt.Errorf("decode epic: %w", err)
		}
		return projectIDStrings(epic.ProjectIDSet()), nil
	}
	return nil, fmt.Errorf("unknown entity %q", change.Entity)
}

// oneProject wraps a single owning project, dropping the empty id a missing row
// reports so the caller sees "names no project" rather than a blank membership.
func oneProject(projectID string, err error) ([]string, error) {
	if err != nil {
		return nil, err
	}
	return projectSet(projectID), nil
}

// projectSet is a single owning project as a set. An empty id means the payload
// named none, which the caller rejects for an upsert — the same answer the
// single-project engine gave before the set existed.
func projectSet(projectID string) []string {
	if projectID == "" {
		return nil
	}
	return []string{projectID}
}

func projectIDStrings(ids []entities.ProjectID) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if id != "" {
			out = append(out, string(id))
		}
	}
	return out
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

func projectOfSprintMember(ctx context.Context, q entities.Querier, memberID string) (string, error) {
	member, err := entities.NewSprintMemberRepository(q).GetByID(ctx, entities.SprintMemberID(memberID))
	if err != nil || member == nil {
		return "", err
	}
	return string(member.ProjectID), nil
}

// canWrite reports whether the caller may write this change. A project that
// does not exist yet has no membership rows, so the payload's member list is
// what authorizes its creation — which keeps creation open to anyone who names
// themselves a member without letting anyone join a project they cannot see.
//
// An epic is the one entity with more than one project: the caller must be a
// member of every project it names, so a device cannot plant an epic in a
// project it cannot see. A project that does not exist yet is an error rather
// than a refusal, because the project may still arrive and a refusal is
// terminal on the client.
func canWrite(ctx context.Context, q entities.Querier, userID string, change entities.PushChange, projectIDs []string) (bool, error) {
	if change.Entity == entities.OutboxEntityProject && change.Operation != entities.OutboxOperationDelete {
		exists, err := projectExists(ctx, q, projectIDs[0])
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

	if change.Entity == entities.OutboxEntityEpic {
		for _, projectID := range projectIDs {
			exists, err := projectExists(ctx, q, projectID)
			if err != nil {
				return false, err
			}
			if !exists {
				return false, fmt.Errorf("epic names project %s, which does not exist yet", projectID)
			}
			member, err := isMember(ctx, q, userID, projectID)
			if err != nil {
				return false, err
			}
			if !member {
				return false, nil
			}
		}
		return true, nil
	}

	return isMember(ctx, q, userID, projectIDs[0])
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
	case entities.OutboxEntitySprintMember:
		row, err := entities.NewSprintMemberRepository(q).GetByID(ctx, entities.SprintMemberID(change.EntityID))
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
	case entities.OutboxEntityEpic:
		row, err := entities.NewEpicRepository(q).GetByID(ctx, entities.EpicID(change.EntityID))
		if err != nil || row == nil {
			return "", err
		}
		return row.Version, nil
	}
	return "", nil
}

// syncOps is the per-entity wiring applyUpsert needs. Every queued change
// follows the same sequence — delete, decode, load, create or compare-and-swap
// — and only the repository and the fields the server owns differ. Keeping that
// difference in a table rather than five copies of the sequence is what stops
// the version rule drifting between entities.
type syncOps[T any] struct {
	name   string
	decode func([]byte) (*T, error)
	load   func(context.Context, string) (*T, error)
	create func(context.Context, *T) error
	update func(context.Context, *T) error
	remove func(context.Context, string) error
	setID  func(*T, string)
	// adopt copies the fields the server owns — id, creation time and the
	// version this update compares against — from the stored row onto the
	// payload's copy, so a client cannot rewrite them.
	adopt   func(existing, incoming *T)
	version func(*T) string
	// onCreate and onDelete are optional: a new sprint member needs a
	// timestamp default, and an attachment's bytes go with its row.
	onCreate func(*T)
	onDelete func(string)
}

func applyUpsert[T any](ctx context.Context, change entities.PushChange, ops syncOps[T]) (string, error) {
	if change.Operation == entities.OutboxOperationDelete {
		if ops.onDelete != nil {
			ops.onDelete(change.EntityID)
		}
		return "", ops.remove(ctx, change.EntityID)
	}

	entity, err := ops.decode(change.Payload)
	if err != nil {
		return "", fmt.Errorf("decode %s: %w", ops.name, err)
	}
	existing, err := ops.load(ctx, change.EntityID)
	if err != nil {
		return "", err
	}
	if existing == nil {
		ops.setID(entity, change.EntityID)
		if ops.onCreate != nil {
			ops.onCreate(entity)
		}
		if err := ops.create(ctx, entity); err != nil {
			return "", err
		}
		return ops.version(entity), nil
	}
	if change.Version != "" && ops.version(existing) != change.Version {
		return "", &versionConflict{current: ops.version(existing)}
	}
	ops.adopt(existing, entity)
	if err := ops.update(ctx, entity); err != nil {
		return "", err
	}
	return ops.version(entity), nil
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
		// A repository URL can ride a create, and only a create: the project does
		// not exist yet, so there is no admin route that could have set it and the
		// payload is the only place it can come from. An update below still
		// preserves the stored value, so a membership push cannot change a URL that
		// is already set. Validated for the same reason the admin route validates
		// its own — the value reaches the agent brief.
		repoURL, err := entities.NormalizeRepoURL(project.RepoURL)
		if err != nil {
			return "", &invalidProjectMetadata{err}
		}
		project.RepoURL = repoURL
		if change.ProjectRepoURL != nil {
			project.RepoURL = *change.ProjectRepoURL
		}
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
	// A membership push cannot see or change the repository URL, because the
	// payload never carries it. Preserve the stored value unless an admin route
	// explicitly supplied a new one.
	project.RepoURL = existing.RepoURL
	if change.ProjectRepoURL != nil {
		project.RepoURL = *change.ProjectRepoURL
	}
	if err := projects.Update(ctx, project); err != nil {
		return "", err
	}
	return project.Version, nil
}

func applyWorkItem(ctx context.Context, workItems *entities.WorkItemRepository, change entities.PushChange) (string, error) {
	return applyUpsert(ctx, change, syncOps[entities.WorkItem]{
		name:   "work item",
		decode: entities.WorkItemFromJSON,
		load: func(ctx context.Context, id string) (*entities.WorkItem, error) {
			return workItems.GetByID(ctx, entities.WorkItemID(id))
		},
		create: workItems.Create,
		update: workItems.Update,
		remove: func(ctx context.Context, id string) error {
			return workItems.Delete(ctx, entities.WorkItemID(id))
		},
		setID: func(item *entities.WorkItem, id string) { item.ID = entities.WorkItemID(id) },
		adopt: func(existing, incoming *entities.WorkItem) {
			incoming.ID = existing.ID
			incoming.CreatedAt = existing.CreatedAt
			incoming.Version = existing.Version
		},
		version: func(item *entities.WorkItem) string { return item.Version },
	})
}

func applySprint(ctx context.Context, sprints *entities.SprintRepository, change entities.PushChange) (string, error) {
	return applyUpsert(ctx, change, syncOps[entities.Sprint]{
		name:   "sprint",
		decode: entities.SprintFromJSON,
		load: func(ctx context.Context, id string) (*entities.Sprint, error) {
			return sprints.GetByID(ctx, entities.SprintID(id))
		},
		create: sprints.Create,
		update: sprints.Update,
		remove: func(ctx context.Context, id string) error {
			return sprints.Delete(ctx, entities.SprintID(id))
		},
		setID: func(sprint *entities.Sprint, id string) { sprint.ID = entities.SprintID(id) },
		adopt: func(existing, incoming *entities.Sprint) {
			incoming.ID = existing.ID
			incoming.CreatedAt = existing.CreatedAt
			incoming.Version = existing.Version
		},
		version: func(sprint *entities.Sprint) string { return sprint.Version },
	})
}

// applySprintMember never trusts the client to name the row's sprint: the
// authoritative id is change.EntityID, and the payload's capacity is floored
// in the repository. A re-push of the same sprint/user pair updates one row.
func applySprintMember(ctx context.Context, members *entities.SprintMemberRepository, change entities.PushChange) (string, error) {
	return applyUpsert(ctx, change, syncOps[entities.SprintMember]{
		name:   "sprint member",
		decode: entities.SprintMemberFromJSON,
		load: func(ctx context.Context, id string) (*entities.SprintMember, error) {
			return members.GetByID(ctx, entities.SprintMemberID(id))
		},
		create: members.Create,
		update: members.Update,
		remove: func(ctx context.Context, id string) error {
			return members.Delete(ctx, entities.SprintMemberID(id))
		},
		setID: func(member *entities.SprintMember, id string) { member.ID = entities.SprintMemberID(id) },
		onCreate: func(member *entities.SprintMember) {
			if member.CreatedAt == 0 {
				member.CreatedAt = entities.Now()
			}
		},
		adopt: func(existing, incoming *entities.SprintMember) {
			incoming.ID = existing.ID
			incoming.CreatedAt = existing.CreatedAt
			incoming.Version = existing.Version
		},
		version: func(member *entities.SprintMember) string { return member.Version },
	})
}

func applyComment(ctx context.Context, comments *entities.CommentRepository, change entities.PushChange) (string, error) {
	return applyUpsert(ctx, change, syncOps[entities.Comment]{
		name:   "comment",
		decode: entities.CommentFromJSON,
		load: func(ctx context.Context, id string) (*entities.Comment, error) {
			return comments.GetByID(ctx, entities.CommentID(id))
		},
		create: comments.Create,
		update: comments.Update,
		remove: func(ctx context.Context, id string) error {
			return comments.Delete(ctx, entities.CommentID(id))
		},
		setID: func(comment *entities.Comment, id string) { comment.ID = entities.CommentID(id) },
		adopt: func(existing, incoming *entities.Comment) {
			incoming.ID = existing.ID
			incoming.CreatedAt = existing.CreatedAt
			incoming.Version = existing.Version
		},
		version: func(comment *entities.Comment) string { return comment.Version },
	})
}

// applyAttachment mirrors applyComment. On a delete it also removes the on-disk
// blob, because the row's removal is the only signal the blob store gets — a
// failed file delete is best-effort and must not fail the metadata delete.
func applyAttachment(ctx context.Context, attachmentRoot string, attachments *entities.AttachmentRepository, change entities.PushChange) (string, error) {
	return applyUpsert(ctx, change, syncOps[entities.Attachment]{
		name:   "attachment",
		decode: entities.AttachmentFromJSON,
		load: func(ctx context.Context, id string) (*entities.Attachment, error) {
			return attachments.GetByID(ctx, entities.AttachmentID(id))
		},
		create: attachments.Create,
		update: attachments.Update,
		remove: func(ctx context.Context, id string) error {
			return attachments.Delete(ctx, entities.AttachmentID(id))
		},
		setID: func(attachment *entities.Attachment, id string) { attachment.ID = entities.AttachmentID(id) },
		onDelete: func(id string) {
			removeAttachmentBlob(attachmentRoot, id)
		},
		adopt: func(existing, incoming *entities.Attachment) {
			incoming.ID = existing.ID
			incoming.CreatedAt = existing.CreatedAt
			incoming.Version = existing.Version
		},
		version: func(attachment *entities.Attachment) string { return attachment.Version },
	})
}

// applyEpic runs the shared upsert sequence. Its project set is part of the
// payload, so the repository replaces the link rows in the same transaction as
// the epic row; a project added or removed is a single accepted change.
func applyEpic(ctx context.Context, epics *entities.EpicRepository, change entities.PushChange) (string, error) {
	return applyUpsert(ctx, change, syncOps[entities.Epic]{
		name:   "epic",
		decode: entities.EpicFromJSON,
		load: func(ctx context.Context, id string) (*entities.Epic, error) {
			return epics.GetByID(ctx, entities.EpicID(id))
		},
		create: epics.Create,
		update: epics.Update,
		remove: func(ctx context.Context, id string) error {
			return epics.Delete(ctx, entities.EpicID(id))
		},
		setID: func(epic *entities.Epic, id string) { epic.ID = entities.EpicID(id) },
		adopt: func(existing, incoming *entities.Epic) {
			incoming.ID = existing.ID
			incoming.CreatedAt = existing.CreatedAt
			incoming.Version = existing.Version
		},
		version: func(epic *entities.Epic) string { return epic.Version },
	})
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
	// name is the wire spelling the membership-replay marker is keyed by
	// (`entities.GrantScope*`), so a grant can be consumed by the feed that replayed it and
	// no other.
	name     string
	entities []entities.OutboxEntity
	// recipientScoped marks a feed whose rows belong to one user rather than to a
	// project's members. Notifications are the only such feed: their predicate is
	// notifications.recipient_id = the caller, and the project membership query
	// would either leak every member's inbox or return nothing.
	recipientScoped bool
}

// The feeds the client pulls, named after its own `SyncScope` enum. There is no
// `sprints` route on purpose: a sprint reaches a client on `workItems`. Sprint
// membership is newer and has a feed of its own so an installed client, which
// never asks for it, cannot decode a payload with keys it does not declare.
var (
	ScopeProjects = Scope{name: entities.GrantScopeProjects, entities: []entities.OutboxEntity{
		entities.OutboxEntityProject,
	}}
	ScopeWorkItems = Scope{name: entities.GrantScopeWorkItems, entities: []entities.OutboxEntity{
		entities.OutboxEntityWorkItem,
		entities.OutboxEntitySprint,
		entities.OutboxEntityComment,
	}}
	ScopeAttachments = Scope{name: entities.GrantScopeAttachments, entities: []entities.OutboxEntity{
		entities.OutboxEntityAttachment,
	}}
	// A new feed rather than a field on a sprint: installed clients do not
	// request it, so they never decode a payload whose keys they do not declare.
	ScopeSprintMembers = Scope{name: entities.GrantScopeSprintMembers, entities: []entities.OutboxEntity{
		entities.OutboxEntitySprintMember,
	}}
	// A cross-project epic reaches every member of every linked project. It has
	// its own feed because an installed client never asks for it, and because its
	// payload's keys are not the work item's.
	ScopeEpics = Scope{name: entities.GrantScopeEpics, entities: []entities.OutboxEntity{
		entities.OutboxEntityEpic,
	}}
	// A recipient-scoped feed of its own. It carries an entity an installed
	// client never asks for, and its rows are visible to exactly one account.
	ScopeNotifications = Scope{
		name:            "notifications",
		entities:        []entities.OutboxEntity{entities.OutboxEntityNotification},
		recipientScoped: true,
	}
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
	// The predicate and the `LIMIT` are positional too, so the whole query is built
	// once instead of being concatenated from two halves that could drift.
	var query string
	if scope.recipientScoped {
		query = fmt.Sprintf(`
			SELECT id, entity, entity_id, operation, payload, version
			  FROM sync_log
			 WHERE entity IN (%s) AND id > $%d
			   AND EXISTS (SELECT 1 FROM notifications n
			                WHERE n.id = sync_log.entity_id AND n.recipient_id = $%d)
			 ORDER BY id
			 LIMIT $%d`, in, cursorAt, cursorAt+1, cursorAt+2)
	} else {
		query = fmt.Sprintf(`
			SELECT id, entity, entity_id, operation, payload, version
			  FROM sync_log
			 WHERE entity IN (%s) AND id > $%d
			   AND ( EXISTS (SELECT 1 FROM sync_log_projects sp
			                  WHERE sp.log_id = sync_log.id
			                    AND sp.project_id IN (SELECT project_id FROM project_members WHERE user_id = $%d))
			         OR (entity = 'project' AND operation = 'delete') )
			 ORDER BY id
			 LIMIT $%d`, in, cursorAt, cursorAt+1, cursorAt+2)
	}

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
	oldest, err := e.oldestCursor(ctx, scope, userID)
	if err != nil {
		return nil, err
	}
	page.ResyncRequired = after > 0 && (oldest == 0 || after < oldest-1)

	// A membership granted after this device synced is the other thing a cursor cannot
	// fix: everything the new member needs was logged before their cursor existed, and
	// `id > cursor` will never reach it. The grant row says one replay is owed on this
	// feed; an empty cursor *is* that replay, so it retires the grant instead of asking
	// again — which is also what stops this from becoming a resync on every cycle.
	granted, err := e.grantOwed(ctx, userID, scope)
	if err != nil {
		return nil, err
	}
	if granted {
		if after > 0 {
			page.ResyncRequired = true
		} else if err := e.retireGrant(ctx, userID, scope); err != nil {
			return nil, err
		}
	}

	return page, nil
}

// grantOwed reports whether this caller's membership on this feed has never been
// replayed. Written by `entities.stampGrant` when a member is added; see Pull for the
// half of the handshake that answers it.
func (e *Engine) grantOwed(ctx context.Context, userID string, scope Scope) (bool, error) {
	var owed bool
	err := e.db.QueryRowContext(ctx,
		`SELECT EXISTS (SELECT 1 FROM membership_grants WHERE user_id = $1 AND scope = $2)`,
		userID, scope.name).Scan(&owed)
	return owed, err
}

// retireGrant clears the marker for this feed and member, called by the pull that starts
// from an empty cursor: that request is the replay the grant asked for.
//
// The grant for the caller's other feeds is deliberately left alone. They have cursors of
// their own, and each of them has to reach zero once before it is done.
func (e *Engine) retireGrant(ctx context.Context, userID string, scope Scope) error {
	_, err := e.db.ExecContext(ctx,
		`DELETE FROM membership_grants WHERE user_id = $1 AND scope = $2`, userID, scope.name)
	return err
}

// oldestCursor is the smallest id still retained for a scope, or 0 when the
// feed is empty. The (entity, created_at, id) index serves this ordering.
//
// It spans every entity of the scope on purpose: a pruned sprint under the cursor is just as
// lost to a client as a pruned work item, and a check that only looked at work items would
// report a clean sync for a gap it never noticed.
func (e *Engine) oldestCursor(ctx context.Context, scope Scope, userID string) (int64, error) {
	in, args := scope.entityPlaceholders(1)
	var oldest int64
	query := fmt.Sprintf(`SELECT id FROM sync_log WHERE entity IN (%s) ORDER BY created_at, id LIMIT 1`, in)
	if scope.recipientScoped {
		// Pruning is global; the resync check must not be. Another account's
		// pruned inbox is not this caller's gap.
		query = fmt.Sprintf(`SELECT id FROM sync_log
			 WHERE entity IN (%s)
			   AND EXISTS (SELECT 1 FROM notifications n
			                WHERE n.id = sync_log.entity_id AND n.recipient_id = $%d)
			 ORDER BY created_at, id LIMIT 1`, in, len(args)+1)
		args = append(args, userID)
	}
	err := e.db.QueryRowContext(ctx, query, args...).Scan(&oldest)
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

// appendLog writes the change feed row and its visibility set. It takes the
// caller's transaction so the entity write and this row commit together.
//
// Visibility is a set of projects rather than one. Most entities name a single
// project; an epic names every project it spans, so its one log row gets one
// visibility row per linked project and reaches all of their members. The pull
// predicate is then one uniform EXISTS, with no special case for epics.
func appendLog(ctx context.Context, q entities.Querier, projectIDs []string, change entities.PushChange, userID, version string) error {
	var logID int64
	if err := q.QueryRowContext(ctx, `
		INSERT INTO sync_log (entity, entity_id, operation, payload, version, user_id, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING id`,
		string(change.Entity),
		change.EntityID,
		string(change.Operation),
		string(change.Payload),
		version,
		nullableString(userID),
		time.Now().UnixMilli()).Scan(&logID); err != nil {
		return err
	}

	seen := make(map[string]struct{}, len(projectIDs))
	for _, projectID := range projectIDs {
		if projectID == "" {
			continue
		}
		if _, duplicate := seen[projectID]; duplicate {
			continue
		}
		seen[projectID] = struct{}{}
		if _, err := q.ExecContext(ctx,
			`INSERT INTO sync_log_projects (log_id, project_id) VALUES ($1, $2)`,
			logID, projectID); err != nil {
			return err
		}
	}
	return nil
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
