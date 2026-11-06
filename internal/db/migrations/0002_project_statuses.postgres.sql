-- Per-project workflow status catalog (project configuration, like tags) and
-- the item's reference into it.
--
-- Additive on purpose: work_items.status keeps the coarse WorkItemStatus the
-- CHECK constraint and the overdue/archive/board rules read, so renaming or
-- adding a status never changes an item's category. The catalog rides the
-- project payload; the item carries the key.
ALTER TABLE projects ADD COLUMN IF NOT EXISTS statuses TEXT;
ALTER TABLE work_items ADD COLUMN IF NOT EXISTS status_key TEXT;