-- The SQLite spelling of 0008_epics.postgres.sql. See that file for why each
-- object exists; only the type names differ (TEXT for UUID, INTEGER for BIGSERIAL).
CREATE TABLE IF NOT EXISTS epics (
    id TEXT PRIMARY KEY,
    title TEXT NOT NULL,
    details TEXT DEFAULT '',
    due_date BIGINT,
    is_archived BOOLEAN DEFAULT FALSE,
    created_at BIGINT,
    updated_at BIGINT,
    version TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS epic_projects (
    epic_id TEXT REFERENCES epics(id) ON DELETE CASCADE,
    project_id TEXT REFERENCES projects(id) ON DELETE CASCADE,
    position INT NOT NULL DEFAULT 0,
    PRIMARY KEY (epic_id, project_id)
);
CREATE INDEX IF NOT EXISTS idx_epic_projects_project ON epic_projects(project_id);

ALTER TABLE work_items ADD COLUMN epic_id TEXT REFERENCES epics(id) ON DELETE SET NULL;
CREATE INDEX IF NOT EXISTS idx_work_items_epic ON work_items(epic_id);

CREATE TABLE IF NOT EXISTS sync_log_projects (
    log_id INTEGER REFERENCES sync_log(id) ON DELETE CASCADE,
    project_id TEXT NOT NULL,
    PRIMARY KEY (log_id, project_id)
);
CREATE INDEX IF NOT EXISTS idx_sync_log_projects_project ON sync_log_projects(project_id, log_id);

DROP INDEX IF EXISTS idx_sync_log_project;
ALTER TABLE sync_log DROP COLUMN project_id;
