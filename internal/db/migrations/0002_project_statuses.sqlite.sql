-- Per-project workflow status catalog (project configuration, like tags) and
-- the item's reference into it.
--
-- Additive on purpose: work_items.status keeps the coarse WorkItemStatus the
-- CHECK constraint and the overdue/archive/board rules read, so renaming or
-- adding a status never changes an item's category. The catalog rides the
-- project payload; the item carries the key.
--
-- SQLite has no ADD COLUMN IF NOT EXISTS; the migration runner skips the
-- "duplicate column name" error when this re-runs over an adopted schema.
ALTER TABLE projects ADD COLUMN statuses TEXT;
ALTER TABLE work_items ADD COLUMN status_key TEXT;