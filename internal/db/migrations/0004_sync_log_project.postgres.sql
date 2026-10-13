-- Attribute each change-feed row to a project, so Pull can be scoped to the
-- projects a caller is a member of.
--
-- The column is deliberately not a foreign key: a row must outlive the project
-- it names, or a client would never learn the project was deleted. Rows written
-- before this version are attributed for the `project` entity only; older
-- work-item, sprint and comment rows keep a NULL project_id and are not
-- delivered, which a client recovers from with a full resync.

ALTER TABLE sync_log ADD COLUMN IF NOT EXISTS project_id UUID;

UPDATE sync_log SET project_id = entity_id WHERE entity = 'project' AND project_id IS NULL;

CREATE INDEX IF NOT EXISTS idx_sync_log_project ON sync_log (project_id, id);
