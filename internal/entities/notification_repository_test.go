package entities

import (
	"context"
	"testing"
)

func TestNotificationRepositoryIsRecipientScopedAndIdempotent(t *testing.T) {
	database := newTestDB(t)
	users := NewUserRepository(database.DB)
	notifications := NewNotificationRepository(database.DB)
	ctx := context.Background()

	recipient := mustCreateUser(t, users, "recipient@example.com")
	other := mustCreateUser(t, users, "other@example.com")

	n := &Notification{
		UserID:     recipient.ID,
		Kind:       NotificationKindTaskAssigned,
		Payload:    `{"workItemId":"wi-1"}`,
		SourceKind: NotificationSourceWorkItem,
		SourceID:   "wi-1:v-1",
	}
	inserted, err := notifications.Create(ctx, n)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if !inserted {
		t.Fatal("the first insert reported no row")
	}

	// The same source event replayed is a no-op, not a second row.
	duplicate := &Notification{
		UserID:     recipient.ID,
		Kind:       NotificationKindTaskAssigned,
		Payload:    `{"workItemId":"wi-1"}`,
		SourceKind: NotificationSourceWorkItem,
		SourceID:   "wi-1:v-1",
	}
	inserted, err = notifications.Create(ctx, duplicate)
	if err != nil {
		t.Fatalf("duplicate create: %v", err)
	}
	if inserted {
		t.Error("a replayed source event created a second notification")
	}

	rows, err := notifications.ListByRecipient(ctx, recipient.ID, true)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("recipient rows = %d, want 1", len(rows))
	}
	if rows[0].IsRead {
		t.Error("a generated notification came back read")
	}

	// Somebody else cannot read it, and the answer is the same as for an unknown
	// id — a rejected push must not become an existence oracle.
	changed, err := notifications.SetRead(ctx, n.ID, other.ID, true, Now())
	if err != nil {
		t.Fatalf("set read by another user: %v", err)
	}
	if changed {
		t.Error("another user changed somebody else's read flag")
	}
	changed, err = notifications.SetRead(ctx, n.ID, recipient.ID, true, Now())
	if err != nil {
		t.Fatalf("set read: %v", err)
	}
	if !changed {
		t.Fatal("the recipient could not mark their own notification read")
	}

	// A dismissed row is gone from the default listing and cannot be read again.
	changed, err = notifications.Dismiss(ctx, n.ID, recipient.ID, Now())
	if err != nil {
		t.Fatalf("dismiss: %v", err)
	}
	if !changed {
		t.Fatal("the recipient could not dismiss their own notification")
	}
	rows, err = notifications.ListByRecipient(ctx, recipient.ID, false)
	if err != nil {
		t.Fatalf("list after dismiss: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("live rows after dismiss = %d, want 0", len(rows))
	}
	changed, err = notifications.SetRead(ctx, n.ID, recipient.ID, false, Now())
	if err != nil {
		t.Fatalf("read a dismissed row: %v", err)
	}
	if changed {
		t.Error("a dismissed notification was marked unread")
	}
}

func TestNotificationWireObjectCarriesOnlyTheClientKeys(t *testing.T) {
	database := newTestDB(t)
	users := NewUserRepository(database.DB)
	notifications := NewNotificationRepository(database.DB)
	ctx := context.Background()

	recipient := mustCreateUser(t, users, "wire@example.com")
	n := &Notification{
		UserID:     recipient.ID,
		Kind:       NotificationKindCommentAdded,
		Payload:    `{"workItemId":"wi-2","commentPreview":"hello"}`,
		SourceKind: NotificationSourceComment,
		SourceID:   "c-1",
	}
	if _, err := notifications.Create(ctx, n); err != nil {
		t.Fatalf("create: %v", err)
	}

	encoded, err := n.ToJSON()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	want := []string{`"id"`, `"userID"`, `"kind"`, `"payload"`, `"isRead"`, `"createdAt"`, `"updatedAt"`}
	for _, key := range want {
		if !bytesContains(encoded, key) {
			t.Errorf("payload %s is missing %s", encoded, key)
		}
	}
	for _, key := range []string{`"sourceKind"`, `"sourceID"`, `"version"`, `"dismissedAt"`} {
		if bytesContains(encoded, key) {
			t.Errorf("payload %s gained storage-only key %s", encoded, key)
		}
	}
}

func bytesContains(haystack []byte, needle string) bool {
	return len(needle) > 0 && stringContains(string(haystack), needle)
}

func stringContains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
