-- An epic has one owner. The column is nullable so a row written before this
-- version, or one whose owner account was removed, still reads; ON DELETE SET
-- NULL keeps the epic when the user goes away.
ALTER TABLE epics ADD COLUMN IF NOT EXISTS owner_id UUID REFERENCES users(id) ON DELETE SET NULL;

-- Existing epics predate the field. Give them the oldest non-deleted admin so
-- every legacy row has one answer, independent of which client first pushed it.
-- An inactive admin still administers; only 'deleted' means gone.
UPDATE epics
   SET owner_id = (
       SELECT id FROM users
        WHERE role = 'admin' AND COALESCE(status, '') <> 'deleted'
        ORDER BY created_at, id
        LIMIT 1
   )
 WHERE owner_id IS NULL;
