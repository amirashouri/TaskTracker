package sync

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/amirashouri/tasktracker-server/internal/entities"
)

func workItemChange(t *testing.T, entryID string, item *entities.WorkItem, operation entities.OutboxOperation, version string) entities.PushChange {
	t.Helper()
	payload, err := json.Marshal(item)
	if err != nil {
		t.Fatalf("marshal work item: %v", err)
	}
	return entities.PushChange{
		ID:        entryID,
		Entity:    entities.OutboxEntityWorkItem,
		EntityID:  string(item.ID),
		Operation: operation,
		Payload:   payload,
		Version:   version,
	}
}

func commentChange(t *testing.T, entryID string, comment *entities.Comment, operation entities.OutboxOperation, version string) entities.PushChange {
	t.Helper()
	payload, err := json.Marshal(comment)
	if err != nil {
		t.Fatalf("marshal comment: %v", err)
	}
	return entities.PushChange{
		ID:        entryID,
		Entity:    entities.OutboxEntityComment,
		EntityID:  string(comment.ID),
		Operation: operation,
		Payload:   payload,
		Version:   version,
	}
}

func TestAssignmentWritesOneNotificationForTheAssignee(t *testing.T) {
	engine, users, _, _, ownerID := newTestEngine(t)
	ctx := context.Background()

	assignee := createUser(t, users, "assignee@example.com")
	project := newProject(ownerID, "11111111-2222-3333-4444-555555555555")
	project.MemberIDs = append(project.MemberIDs, assignee.ID)
	pushOne(t, engine, ownerID, projectChange(t, "p-1", project, entities.OutboxOperationUpsert, ""))

	item := &entities.WorkItem{
		ID:         "aaaaaaaa-2222-3333-4444-555555555555",
		ProjectID:  project.ID,
		Key:        "TT-1",
		Title:      "Fix the login bug",
		Status:     entities.WorkItemStatusTodo,
		Priority:   entities.PriorityHigh,
		AssigneeID: &assignee.ID,
	}
	if res := pushOne(t, engine, ownerID, workItemChange(t, "w-1", item, entities.OutboxOperationUpsert, "")); res.Status != StatusAccepted {
		t.Fatalf("work item push = %s (%s)", res.Status, res.Message)
	}

	rows, err := entities.NewNotificationRepository(engine.db).ListByRecipient(ctx, assignee.ID, true)
	if err != nil {
		t.Fatalf("list notifications: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("notifications = %d, want 1", len(rows))
	}
	if rows[0].Kind != entities.NotificationKindTaskAssigned {
		t.Errorf("kind = %q, want %q", rows[0].Kind, entities.NotificationKindTaskAssigned)
	}
	if rows[0].UserID != assignee.ID {
		t.Errorf("recipient = %s, want %s", rows[0].UserID, assignee.ID)
	}

	var payload entities.NotificationPayload
	if err := json.Unmarshal([]byte(rows[0].Payload), &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if payload.WorkItemID != string(item.ID) || payload.TaskTitle != item.Title {
		t.Errorf("payload = %+v, want the assigned item", payload)
	}
	if payload.ActorName != "sync" {
		// The test owner's email is sync@example.com and it has no display name.
		t.Errorf("actor name = %q, want the assigning user", payload.ActorName)
	}
}

func TestSavingTheSameAssigneeDoesNotNotifyAgain(t *testing.T) {
	engine, users, _, workItems, ownerID := newTestEngine(t)
	ctx := context.Background()

	assignee := createUser(t, users, "same@example.com")
	project := newProject(ownerID, "11111111-2222-3333-4444-666666666666")
	project.MemberIDs = append(project.MemberIDs, assignee.ID)
	pushOne(t, engine, ownerID, projectChange(t, "p-1", project, entities.OutboxOperationUpsert, ""))

	item := &entities.WorkItem{
		ID:         "aaaaaaaa-2222-3333-4444-666666666666",
		ProjectID:  project.ID,
		Key:        "TT-1",
		Title:      "Unchanged assignee",
		Status:     entities.WorkItemStatusTodo,
		Priority:   entities.PriorityMedium,
		AssigneeID: &assignee.ID,
	}
	pushOne(t, engine, ownerID, workItemChange(t, "w-1", item, entities.OutboxOperationUpsert, ""))

	stored, err := workItems.GetByID(ctx, item.ID)
	if err != nil || stored == nil {
		t.Fatalf("reload item: %v", err)
	}
	stored.Title = "Edited title"
	pushOne(t, engine, ownerID, workItemChange(t, "w-2", stored, entities.OutboxOperationUpsert, stored.Version))

	rows, err := entities.NewNotificationRepository(engine.db).ListByRecipient(ctx, assignee.ID, true)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 1 {
		t.Errorf("notifications = %d, want 1: an edit that keeps the assignee is not a new assignment", len(rows))
	}
}

func TestSelfAssignmentNotifiesNobody(t *testing.T) {
	engine, _, _, _, ownerID := newTestEngine(t)
	ctx := context.Background()

	project := newProject(ownerID, "11111111-2222-3333-4444-777777777777")
	pushOne(t, engine, ownerID, projectChange(t, "p-1", project, entities.OutboxOperationUpsert, ""))

	owner := entities.UserID(ownerID)
	item := &entities.WorkItem{
		ID:         "aaaaaaaa-2222-3333-4444-777777777777",
		ProjectID:  project.ID,
		Key:        "TT-1",
		Title:      "Assign to myself",
		Status:     entities.WorkItemStatusTodo,
		Priority:   entities.PriorityMedium,
		AssigneeID: &owner,
	}
	pushOne(t, engine, ownerID, workItemChange(t, "w-1", item, entities.OutboxOperationUpsert, ""))

	rows, err := entities.NewNotificationRepository(engine.db).ListByRecipient(ctx, owner, true)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("notifications = %d, want 0: assigning yourself is not an event", len(rows))
	}
}

func TestCommentOnAnAssignedItemNotifiesTheAssignee(t *testing.T) {
	engine, users, _, _, ownerID := newTestEngine(t)
	ctx := context.Background()

	assignee := createUser(t, users, "commented@example.com")
	project := newProject(ownerID, "11111111-2222-3333-4444-888888888888")
	project.MemberIDs = append(project.MemberIDs, assignee.ID)
	pushOne(t, engine, ownerID, projectChange(t, "p-1", project, entities.OutboxOperationUpsert, ""))

	item := &entities.WorkItem{
		ID:         "aaaaaaaa-2222-3333-4444-888888888888",
		ProjectID:  project.ID,
		Key:        "TT-1",
		Title:      "Has a thread",
		Status:     entities.WorkItemStatusTodo,
		Priority:   entities.PriorityMedium,
		AssigneeID: &assignee.ID,
	}
	pushOne(t, engine, ownerID, workItemChange(t, "w-1", item, entities.OutboxOperationUpsert, ""))

	comment := &entities.Comment{
		ID:         "cccccccc-2222-3333-4444-888888888888",
		WorkItemID: item.ID,
		AuthorID:   entities.UserID(ownerID),
		Body:       "Any progress?\n\n  The release is tomorrow.",
	}
	if res := pushOne(t, engine, ownerID, commentChange(t, "c-1", comment, entities.OutboxOperationUpsert, "")); res.Status != StatusAccepted {
		t.Fatalf("comment push = %s (%s)", res.Status, res.Message)
	}

	rows, err := entities.NewNotificationRepository(engine.db).ListByRecipient(ctx, assignee.ID, true)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	// The assignment that created the item is a notification of its own; this
	// test is about the comment, so it is selected by kind rather than by count.
	var note *entities.Notification
	for _, row := range rows {
		if row.Kind == entities.NotificationKindCommentAdded {
			note = row
		}
	}
	if note == nil {
		t.Fatalf("notifications = %+v, want a comment_added row", rows)
	}
	var payload entities.NotificationPayload
	if err := json.Unmarshal([]byte(note.Payload), &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if payload.CommentID != string(comment.ID) {
		t.Errorf("comment id = %q, want %q", payload.CommentID, comment.ID)
	}
	if payload.CommentPreview != "Any progress? The release is tomorrow." {
		t.Errorf("preview = %q, want the comment flattened to one line", payload.CommentPreview)
	}
}

func TestCommentByTheAssigneeNotifiesNobody(t *testing.T) {
	engine, users, _, _, ownerID := newTestEngine(t)
	ctx := context.Background()

	assignee := createUser(t, users, "self-comment@example.com")
	project := newProject(ownerID, "11111111-2222-3333-4444-999999999999")
	project.MemberIDs = append(project.MemberIDs, assignee.ID)
	pushOne(t, engine, ownerID, projectChange(t, "p-1", project, entities.OutboxOperationUpsert, ""))

	item := &entities.WorkItem{
		ID:         "aaaaaaaa-2222-3333-4444-999999999999",
		ProjectID:  project.ID,
		Key:        "TT-1",
		Title:      "Own thread",
		Status:     entities.WorkItemStatusTodo,
		Priority:   entities.PriorityMedium,
		AssigneeID: &assignee.ID,
	}
	pushOne(t, engine, ownerID, workItemChange(t, "w-1", item, entities.OutboxOperationUpsert, ""))

	comment := &entities.Comment{
		ID:         "cccccccc-2222-3333-4444-999999999999",
		WorkItemID: item.ID,
		AuthorID:   assignee.ID,
		Body:       "Working on it.",
	}
	pushOne(t, engine, string(assignee.ID), commentChange(t, "c-1", comment, entities.OutboxOperationUpsert, ""))

	rows, err := entities.NewNotificationRepository(engine.db).ListByRecipient(ctx, assignee.ID, true)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	// The assignment notification is expected; the assignee's own comment is not.
	if len(rows) != 1 || rows[0].Kind != entities.NotificationKindTaskAssigned {
		t.Errorf("notifications = %+v, want only the assignment: the assignee commenting is not an event", rows)
	}
}

func TestNotificationPullIsRecipientScoped(t *testing.T) {
	engine, users, _, _, ownerID := newTestEngine(t)
	ctx := context.Background()

	assignee := createUser(t, users, "scope@example.com")
	project := newProject(ownerID, "11111111-2222-3333-4444-aaaaaaaaaaaa")
	project.MemberIDs = append(project.MemberIDs, assignee.ID)
	pushOne(t, engine, ownerID, projectChange(t, "p-1", project, entities.OutboxOperationUpsert, ""))

	item := &entities.WorkItem{
		ID:         "aaaaaaaa-2222-3333-4444-aaaaaaaaaaaa",
		ProjectID:  project.ID,
		Key:        "TT-1",
		Title:      "Scoped",
		Status:     entities.WorkItemStatusTodo,
		Priority:   entities.PriorityMedium,
		AssigneeID: &assignee.ID,
	}
	pushOne(t, engine, ownerID, workItemChange(t, "w-1", item, entities.OutboxOperationUpsert, ""))

	theirs, err := engine.Pull(ctx, string(assignee.ID), ScopeNotifications, "", 0)
	if err != nil {
		t.Fatalf("pull as recipient: %v", err)
	}
	if len(theirs.Changes) != 1 {
		t.Fatalf("recipient pull = %d changes, want 1", len(theirs.Changes))
	}
	if theirs.Changes[0].Entity != entities.OutboxEntityNotification {
		t.Errorf("entity = %q, want notification", theirs.Changes[0].Entity)
	}

	owners, err := engine.Pull(ctx, ownerID, ScopeNotifications, "", 0)
	if err != nil {
		t.Fatalf("pull as owner: %v", err)
	}
	if len(owners.Changes) != 0 {
		t.Errorf("owner pull = %d changes, want 0: the inbox belongs to its recipient", len(owners.Changes))
	}
}

func TestNotificationReadPushChangesOnlyReadState(t *testing.T) {
	engine, users, _, _, ownerID := newTestEngine(t)
	ctx := context.Background()

	assignee := createUser(t, users, "read@example.com")
	project := newProject(ownerID, "11111111-2222-3333-4444-bbbbbbbbbbbb")
	project.MemberIDs = append(project.MemberIDs, assignee.ID)
	pushOne(t, engine, ownerID, projectChange(t, "p-1", project, entities.OutboxOperationUpsert, ""))

	item := &entities.WorkItem{
		ID:         "aaaaaaaa-2222-3333-4444-bbbbbbbbbbbb",
		ProjectID:  project.ID,
		Key:        "TT-1",
		Title:      "Read me",
		Status:     entities.WorkItemStatusTodo,
		Priority:   entities.PriorityMedium,
		AssigneeID: &assignee.ID,
	}
	pushOne(t, engine, ownerID, workItemChange(t, "w-1", item, entities.OutboxOperationUpsert, ""))

	rows, err := entities.NewNotificationRepository(engine.db).ListByRecipient(ctx, assignee.ID, true)
	if err != nil || len(rows) != 1 {
		t.Fatalf("notification not generated: %v (%d rows)", err, len(rows))
	}
	id := rows[0].ID

	read := entities.PushChange{
		ID:        "read-1",
		Entity:    entities.OutboxEntityNotification,
		EntityID:  string(id),
		Operation: entities.OutboxOperationUpsert,
		Payload:   []byte(`{"isRead":true}`),
	}
	if res := pushOne(t, engine, ownerID, read); res.Status != StatusRejected {
		t.Errorf("another user marking the read flag = %s, want rejected", res.Status)
	}
	if res := pushOne(t, engine, string(assignee.ID), read); res.Status != StatusAccepted {
		t.Fatalf("the recipient marking it read = %s (%s)", res.Status, res.Message)
	}

	// The feed re-serves the whole stored document, not the patch: a client sink
	// decodes a notification, not a bare isRead.
	page, err := engine.Pull(ctx, string(assignee.ID), ScopeNotifications, "", 0)
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	if len(page.Changes) != 2 {
		t.Fatalf("pull = %d changes, want the create and the read update", len(page.Changes))
	}
	updated, err := entities.NotificationFromJSON(page.Changes[1].Payload)
	if err != nil {
		t.Fatalf("the read update is not a notification document: %v", err)
	}
	if !updated.IsRead {
		t.Error("the pulled read update is still unread")
	}
	if updated.Kind != entities.NotificationKindTaskAssigned || updated.UserID != assignee.ID {
		t.Errorf("read update rewrote the notification: %+v", updated)
	}
}

func TestNotificationPushCannotCreateOne(t *testing.T) {
	engine, _, _, _, ownerID := newTestEngine(t)

	fabricated := entities.PushChange{
		ID:        "fake-1",
		Entity:    entities.OutboxEntityNotification,
		EntityID:  "dddddddd-2222-3333-4444-cccccccccccc",
		Operation: entities.OutboxOperationUpsert,
		Payload:   []byte(`{"isRead":false}`),
	}
	if res := pushOne(t, engine, ownerID, fabricated); res.Status != StatusRejected {
		t.Errorf("fabricated notification = %s, want rejected", res.Status)
	}
}

func TestDismissIsRecipientScopedAndIdempotent(t *testing.T) {
	engine, users, _, _, ownerID := newTestEngine(t)
	ctx := context.Background()

	assignee := createUser(t, users, "dismiss@example.com")
	project := newProject(ownerID, "11111111-2222-3333-4444-cccccccccccc")
	project.MemberIDs = append(project.MemberIDs, assignee.ID)
	pushOne(t, engine, ownerID, projectChange(t, "p-1", project, entities.OutboxOperationUpsert, ""))

	item := &entities.WorkItem{
		ID:         "aaaaaaaa-2222-3333-4444-cccccccccccc",
		ProjectID:  project.ID,
		Key:        "TT-1",
		Title:      "Dismiss me",
		Status:     entities.WorkItemStatusTodo,
		Priority:   entities.PriorityMedium,
		AssigneeID: &assignee.ID,
	}
	pushOne(t, engine, ownerID, workItemChange(t, "w-1", item, entities.OutboxOperationUpsert, ""))

	rows, err := entities.NewNotificationRepository(engine.db).ListByRecipient(ctx, assignee.ID, true)
	if err != nil || len(rows) != 1 {
		t.Fatalf("notification not generated: %v", err)
	}
	id := string(rows[0].ID)

	dismiss := entities.PushChange{
		ID:        "dismiss-1",
		Entity:    entities.OutboxEntityNotification,
		EntityID:  id,
		Operation: entities.OutboxOperationDelete,
		Payload:   json.RawMessage(`"` + id + `"`),
	}
	// Somebody else's delete is a no-op that is still accepted, so it cannot be
	// used to probe an inbox.
	if res := pushOne(t, engine, ownerID, dismiss); res.Status != StatusAccepted {
		t.Errorf("another user's dismiss = %s, want accepted", res.Status)
	}
	live, err := entities.NewNotificationRepository(engine.db).ListByRecipient(ctx, assignee.ID, false)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(live) != 1 {
		t.Error("another user dismissed the recipient's notification")
	}

	if res := pushOne(t, engine, string(assignee.ID), dismiss); res.Status != StatusAccepted {
		t.Fatalf("recipient dismiss = %s (%s)", res.Status, res.Message)
	}
	// A replayed dismiss completes the same state twice.
	if res := pushOne(t, engine, string(assignee.ID), dismiss); res.Status != StatusAccepted {
		t.Errorf("replayed dismiss = %s, want accepted", res.Status)
	}
	live, err = entities.NewNotificationRepository(engine.db).ListByRecipient(ctx, assignee.ID, false)
	if err != nil {
		t.Fatalf("list after dismiss: %v", err)
	}
	if len(live) != 0 {
		t.Errorf("live notifications = %d, want 0", len(live))
	}
}
