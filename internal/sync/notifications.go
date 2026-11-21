package sync

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"

	"github.com/amirashouri/tasktracker-server/internal/entities"
)

// The inbox is derived from writes the engine already applies. Everything in
// this file is best-effort by design: a notification that cannot be written is
// logged and dropped, never allowed to change the verdict of the edit that
// caused it. A user's assignment must not fail because their recipient row
// could not be inserted.

const notificationPreviewRunes = 160

// notificationSnapshot is the small piece of prior state generation needs,
// read before the change is applied. Reading after the write cannot tell a
// changed assignee from an unchanged one, and cannot tell a new comment from an
// edit.
type notificationSnapshot struct {
	existed      bool
	assignee     *entities.UserID
	isNewComment bool
}

func notificationSnapshotFor(ctx context.Context, q entities.Querier, change entities.PushChange) notificationSnapshot {
	if change.Operation != entities.OutboxOperationUpsert {
		return notificationSnapshot{}
	}

	switch change.Entity {
	case entities.OutboxEntityWorkItem:
		existing, err := entities.NewWorkItemRepository(q).GetByID(ctx, entities.WorkItemID(change.EntityID))
		if err != nil {
			slog.Warn("notification snapshot: read work item", "id", change.EntityID, "error", err)
			return notificationSnapshot{}
		}
		if existing == nil {
			return notificationSnapshot{}
		}
		snapshot := notificationSnapshot{existed: true}
		if existing.AssigneeID != nil {
			assignee := *existing.AssigneeID
			snapshot.assignee = &assignee
		}
		return snapshot

	case entities.OutboxEntityComment:
		existing, err := entities.NewCommentRepository(q).GetByID(ctx, entities.CommentID(change.EntityID))
		if err != nil {
			slog.Warn("notification snapshot: read comment", "id", change.EntityID, "error", err)
			return notificationSnapshot{}
		}
		return notificationSnapshot{isNewComment: existing == nil}
	}

	return notificationSnapshot{}
}

// notificationForChange builds the one notification a change may produce, or
// nil. The stored rows are read rather than the payload: a client-encoded
// payload is authoritative for the write, but the stored item is what the
// notification describes and what the recipient can open.
func notificationForChange(
	ctx context.Context,
	q entities.Querier,
	actorID string,
	change entities.PushChange,
	snapshot notificationSnapshot,
) *entities.Notification {
	switch change.Entity {
	case entities.OutboxEntityWorkItem:
		if change.Operation != entities.OutboxOperationUpsert {
			return nil
		}
		item, err := entities.NewWorkItemRepository(q).GetByID(ctx, entities.WorkItemID(change.EntityID))
		if err != nil || item == nil || item.AssigneeID == nil {
			return nil
		}
		recipient := *item.AssigneeID
		if string(recipient) == actorID {
			return nil
		}
		// Only a new assignment is an event. An edit that leaves the assignee
		// alone is not, and generation is also guarded by the source index.
		if snapshot.existed && snapshot.assignee != nil && *snapshot.assignee == recipient {
			return nil
		}
		return assignmentNotification(ctx, q, actorID, item)

	case entities.OutboxEntityComment:
		if change.Operation != entities.OutboxOperationUpsert || !snapshot.isNewComment {
			return nil
		}
		comment, err := entities.NewCommentRepository(q).GetByID(ctx, entities.CommentID(change.EntityID))
		if err != nil || comment == nil {
			return nil
		}
		item, err := entities.NewWorkItemRepository(q).GetByID(ctx, comment.WorkItemID)
		if err != nil || item == nil || item.AssigneeID == nil {
			return nil
		}
		if *item.AssigneeID == comment.AuthorID {
			return nil
		}
		return commentNotification(ctx, q, item, comment)
	}

	return nil
}

func assignmentNotification(
	ctx context.Context,
	q entities.Querier,
	actorID string,
	item *entities.WorkItem,
) *entities.Notification {
	prefix, projectName, err := entities.NewProjectRepository(q).Identity(ctx, item.ProjectID)
	if err != nil {
		slog.Warn("notification: read project identity", "project", item.ProjectID, "error", err)
		return nil
	}

	payload, err := json.Marshal(entities.NotificationPayload{
		WorkItemID:       string(item.ID),
		WorkItemKey:      item.Key,
		TaskTitle:        item.Title,
		ProjectID:        string(item.ProjectID),
		ProjectKeyPrefix: prefix,
		ProjectName:      projectName,
		ActorName:        actorName(ctx, q, entities.UserID(actorID)),
	})
	if err != nil {
		return nil
	}

	return &entities.Notification{
		UserID:     *item.AssigneeID,
		Kind:       entities.NotificationKindTaskAssigned,
		Payload:    string(payload),
		SourceKind: entities.NotificationSourceWorkItem,
		// The new version is unique per write, so a replayed change cannot
		// produce a second row while a later reassignment still can.
		SourceID: string(item.ID) + ":" + item.Version,
	}
}

func commentNotification(
	ctx context.Context,
	q entities.Querier,
	item *entities.WorkItem,
	comment *entities.Comment,
) *entities.Notification {
	prefix, projectName, err := entities.NewProjectRepository(q).Identity(ctx, item.ProjectID)
	if err != nil {
		slog.Warn("notification: read project identity", "project", item.ProjectID, "error", err)
		return nil
	}

	payload, err := json.Marshal(entities.NotificationPayload{
		WorkItemID:       string(item.ID),
		WorkItemKey:      item.Key,
		TaskTitle:        item.Title,
		ProjectID:        string(item.ProjectID),
		ProjectKeyPrefix: prefix,
		ProjectName:      projectName,
		ActorName:        actorName(ctx, q, comment.AuthorID),
		CommentID:        string(comment.ID),
		CommentPreview:   preview(comment.Body),
	})
	if err != nil {
		return nil
	}

	return &entities.Notification{
		UserID:     *item.AssigneeID,
		Kind:       entities.NotificationKindCommentAdded,
		Payload:    string(payload),
		SourceKind: entities.NotificationSourceComment,
		SourceID:   string(comment.ID),
	}
}

// writeGeneratedNotification stores the row and records it on the change feed,
// so the recipient's pull can find it. Both writes share the causing change's
// transaction, so a crash cannot leave a notification nobody is ever told about.
func writeGeneratedNotification(ctx context.Context, q entities.Querier, actorID string, n *entities.Notification) error {
	inserted, err := entities.NewNotificationRepository(q).Create(ctx, n)
	if err != nil || !inserted {
		return err
	}

	payload, err := notificationLogPayload(n)
	if err != nil {
		return err
	}
	return appendLog(ctx, q, "", entities.PushChange{
		ID:        "notification:" + string(n.ID),
		Entity:    entities.OutboxEntityNotification,
		EntityID:  string(n.ID),
		Operation: entities.OutboxOperationUpsert,
		Payload:   payload,
	}, actorID, n.Version)
}

// notificationLogPayload stores the document the way a client push stores its
// own: as a JSON string holding the document. The pull re-serves log bytes
// verbatim, and the client's change type declares `payload` as a String, so an
// object here would fail every installed decoder.
func notificationLogPayload(n *entities.Notification) ([]byte, error) {
	document, err := json.Marshal(n)
	if err != nil {
		return nil, err
	}
	return json.Marshal(string(document))
}

// applyNotificationChange is the push path for a notification. Two rules make
// it unlike every other entity: it never resolves a project, because a
// notification is recipient-scoped rather than membership-scoped, and it can
// only change an existing row the caller owns. A push can never create one.
func (e *Engine) applyNotificationChange(ctx context.Context, userID string, change entities.PushChange) entities.PushResult {
	result := entities.PushResult{EntryID: change.ID}
	if change.EntityID == "" {
		result.Status = StatusRejected
		result.Message = "entityId is required"
		return result
	}

	tx, err := e.db.BeginTx(ctx, nil)
	if err != nil {
		result.Status = StatusRetryLater
		result.Message = err.Error()
		return result
	}
	defer tx.Rollback()

	repo := entities.NewNotificationRepository(tx)
	id := entities.NotificationID(change.EntityID)

	switch change.Operation {
	case entities.OutboxOperationUpsert:
		isRead, err := entities.NotificationReadStateFromJSON(change.Payload)
		if err != nil || isRead == nil {
			result.Status = StatusRejected
			result.Message = "isRead is required"
			return result
		}
		changed, err := repo.SetRead(ctx, id, entities.UserID(userID), *isRead, entities.Now())
		if err != nil {
			result.Status = StatusRetryLater
			result.Message = err.Error()
			return result
		}
		if !changed {
			// Unknown and someone else's row answer the same thing on purpose:
			// a rejected push is terminal, and an oracle here would let a client
			// probe for ids it cannot read.
			result.Status = StatusRejected
			result.Message = "notification not found"
			return result
		}

		stored, err := repo.GetByID(ctx, id)
		if err != nil || stored == nil {
			result.Status = StatusRetryLater
			result.Message = "notification read back failed"
			return result
		}
		// The feed re-serves the stored document, not the client's patch: the
		// client's sink decodes a whole notification and would fail on
		// `{"isRead":true}` alone.
		payload, err := notificationLogPayload(stored)
		if err != nil {
			result.Status = StatusRetryLater
			result.Message = err.Error()
			return result
		}
		if err := appendLog(ctx, tx, "", entities.PushChange{
			Entity:    entities.OutboxEntityNotification,
			EntityID:  change.EntityID,
			Operation: entities.OutboxOperationUpsert,
			Payload:   payload,
		}, userID, stored.Version); err != nil {
			result.Status = StatusRetryLater
			result.Message = err.Error()
			return result
		}

	case entities.OutboxOperationDelete:
		dismissed, err := repo.Dismiss(ctx, id, entities.UserID(userID), entities.Now())
		if err != nil {
			result.Status = StatusRetryLater
			result.Message = err.Error()
			return result
		}
		// Already dismissed, unknown, or somebody else's: all a no-op. The push
		// is still accepted so an offline delete does not dead-letter.
		if dismissed {
			payload, _ := json.Marshal(change.EntityID)
			if err := appendLog(ctx, tx, "", entities.PushChange{
				Entity:    entities.OutboxEntityNotification,
				EntityID:  change.EntityID,
				Operation: entities.OutboxOperationDelete,
				Payload:   payload,
			}, userID, ""); err != nil {
				result.Status = StatusRetryLater
				result.Message = err.Error()
				return result
			}
		}

	default:
		result.Status = StatusRejected
		result.Message = "unknown notification operation"
		return result
	}

	if err := tx.Commit(); err != nil {
		result.Status = StatusRetryLater
		result.Message = err.Error()
		return result
	}
	result.Status = StatusAccepted
	return result
}

// actorName is a label for the card. A missing user is not worth failing the
// notification over; the card falls back to no actor.
func actorName(ctx context.Context, q entities.Querier, id entities.UserID) string {
	name, err := entities.NewUserRepository(q).DisplayName(ctx, id)
	if err != nil {
		return ""
	}
	return name
}

// preview is one line of a comment: whitespace collapsed, then truncated by
// runes so a multi-byte body is not cut mid-character.
func preview(body string) string {
	flat := strings.Join(strings.Fields(body), " ")
	runes := []rune(flat)
	if len(runes) <= notificationPreviewRunes {
		return flat
	}
	return string(runes[:notificationPreviewRunes-1]) + "…"
}
