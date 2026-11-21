package entities

import (
	"context"
	"database/sql"
)

// NotificationRepository owns the notifications table's SQL. The sync engine
// generates rows through it inside the causing transaction, and handles a
// client's read/dismiss pushes through the two owner-scoped writes below.
type NotificationRepository struct {
	db Querier
}

func NewNotificationRepository(db Querier) *NotificationRepository {
	return &NotificationRepository{db: db}
}

const notificationColumns = `id, recipient_id, kind, payload, source_kind, source_id, is_read, dismissed_at, created_at, updated_at, version`

func scanNotification(row scanner, n *Notification) error {
	var dismissedAt sql.NullInt64
	if err := row.Scan(
		&n.ID, &n.UserID, &n.Kind, &n.Payload, &n.SourceKind, &n.SourceID,
		&n.IsRead, &dismissedAt, &n.CreatedAt, &n.UpdatedAt, &n.Version,
	); err != nil {
		return err
	}
	n.DismissedAt = timestampOrNil(dismissedAt)
	return nil
}

// Create inserts one generated notification. It reports whether a row was
// written: a replayed change whose source event is already in the table is a
// no-op, and the caller uses the answer to avoid logging the same change twice.
func (r *NotificationRepository) Create(ctx context.Context, n *Notification) (bool, error) {
	n.ID = orNewID(n.ID, NewNotificationID)
	n.CreatedAt, n.UpdatedAt, n.Version = createStamp(n.CreatedAt)

	result, err := r.db.ExecContext(ctx, `INSERT INTO notifications (`+notificationColumns+`)
	          VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
	          ON CONFLICT (recipient_id, source_kind, source_id) DO NOTHING`,
		n.ID, n.UserID, n.Kind, n.Payload, n.SourceKind, n.SourceID,
		n.IsRead, n.DismissedAt, n.CreatedAt, n.UpdatedAt, n.Version)
	if err != nil {
		return false, err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	return rows > 0, nil
}

// GetByID returns one row, dismissed or not. The engine needs the stored
// document after a read-state write so the change feed re-serves the whole
// notification rather than the client's patch.
func (r *NotificationRepository) GetByID(ctx context.Context, id NotificationID) (*Notification, error) {
	row := r.db.QueryRowContext(ctx, `SELECT `+notificationColumns+` FROM notifications WHERE id = $1`, id)
	return oneOrNil(row, scanNotification)
}

// SetRead changes the read flag on a row the recipient owns. A row belonging to
// somebody else, a dismissed row, or an unknown id all answer false rather than
// telling the caller which case it was.
func (r *NotificationRepository) SetRead(ctx context.Context, id NotificationID, recipient UserID, isRead bool, now Timestamp) (bool, error) {
	result, err := r.db.ExecContext(ctx,
		`UPDATE notifications SET is_read = $1, updated_at = $2, version = $3
		  WHERE id = $4 AND recipient_id = $5 AND dismissed_at IS NULL`,
		isRead, now, newVersion(), id, recipient)
	if err != nil {
		return false, err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	return rows > 0, nil
}

// Dismiss soft-deletes a row the recipient owns. A dismiss is idempotent: an
// already dismissed row answers false, and the engine still accepts the push so
// an offline delete does not dead-letter.
func (r *NotificationRepository) Dismiss(ctx context.Context, id NotificationID, recipient UserID, now Timestamp) (bool, error) {
	result, err := r.db.ExecContext(ctx,
		`UPDATE notifications SET dismissed_at = $1, updated_at = $1, version = $2
		  WHERE id = $3 AND recipient_id = $4 AND dismissed_at IS NULL`,
		now, newVersion(), id, recipient)
	if err != nil {
		return false, err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	return rows > 0, nil
}

// ListByRecipient returns a recipient's rows, newest first. Tests and
// diagnostics use it; the pull path reads the change feed instead, so a row
// created before a device's cursor still reaches it.
func (r *NotificationRepository) ListByRecipient(ctx context.Context, recipient UserID, includeDismissed bool) ([]*Notification, error) {
	query := `SELECT ` + notificationColumns + ` FROM notifications WHERE recipient_id = $1`
	if !includeDismissed {
		query += ` AND dismissed_at IS NULL`
	}
	query += ` ORDER BY created_at DESC, id DESC`

	rows, err := r.db.QueryContext(ctx, query, recipient)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return collectRows(rows, scanNotification)
}

// DisplayName is the name to show beside a notification. A blank display name
// falls back to the email's local part, the same greeting the user object uses.
func (r *UserRepository) DisplayName(ctx context.Context, id UserID) (string, error) {
	var name, email string
	if err := r.db.QueryRowContext(ctx, `SELECT display_name, email FROM users WHERE id = $1`, id).Scan(&name, &email); err != nil {
		return "", err
	}
	user := &User{DisplayName: name, Email: email}
	return user.PreferredGreeting(), nil
}
