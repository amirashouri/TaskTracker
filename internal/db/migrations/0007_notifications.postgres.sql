-- Server-generated inbox rows. A notification belongs to one recipient and is
-- never authored by a client: the sync push path may only change is_read or
-- dismiss the row. The unique source index is what makes generation idempotent
-- when a change is applied twice.
CREATE TABLE IF NOT EXISTS notifications (
    id TEXT PRIMARY KEY,
    recipient_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    kind TEXT NOT NULL,
    payload TEXT NOT NULL,
    source_kind TEXT NOT NULL,
    source_id TEXT NOT NULL,
    is_read BOOLEAN NOT NULL DEFAULT FALSE,
    dismissed_at BIGINT,
    created_at BIGINT NOT NULL,
    updated_at BIGINT NOT NULL,
    version TEXT NOT NULL,
    UNIQUE (recipient_id, source_kind, source_id)
);
CREATE INDEX IF NOT EXISTS idx_notifications_recipient ON notifications(recipient_id, created_at);
