-- Attribute each change-feed row to a project, so Pull can be scoped to the
-- projects a caller is a member of.
--
-- The column is deliberately not a foreign key: a row must outlive the project
-- it names, or a client would never learn the project was deleted. Rows written
-- before this version are attributed for the `project` entity only; older
-- work-item, sprint and comment rows keep a NULL project_id and are not
-- delivered, which a client recovers from with a full resync.
--
-- SQLite does not support ADD COLUMN IF NOT EXISTS, but the migration runner
-- records each version so this statement only runs once per database.

ALTER TABLE sync_log ADD COLUMN project_id TEXT;

UPDATE sync_log SET project_id = entity_id WHERE entity = 'project' AND project_id IS NULL;

CREATE INDEX IF NOT EXISTS idx_sync_log_project ON sync_log (project_id, id);
