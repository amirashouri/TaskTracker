-- An epic groups work items across projects. Its status and progress are derived
-- from those items and never stored, so the row carries only what a user
-- authors. `epic_projects.position` preserves the order the client chose, and
-- the same rows are the set of projects that may see the epic.
CREATE TABLE IF NOT EXISTS epics (
    id UUID PRIMARY KEY,
    title TEXT NOT NULL,
    details TEXT NOT NULL DEFAULT '',
    due_date BIGINT,
    is_archived BOOLEAN NOT NULL DEFAULT FALSE,
    created_at BIGINT,
    updated_at BIGINT,
    version TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS epic_projects (
    epic_id UUID NOT NULL REFERENCES epics(id) ON DELETE CASCADE,
    project_id UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    position INT NOT NULL DEFAULT 0,
    PRIMARY KEY (epic_id, project_id)
);
CREATE INDEX IF NOT EXISTS idx_epic_projects_project ON epic_projects(project_id);

-- One epic per item, and only when the item's project is one of the epic's; the
-- work-item repository refuses a link the join table does not explain.
-- ON DELETE SET NULL keeps the item when its epic is removed.
ALTER TABLE work_items ADD COLUMN IF NOT EXISTS epic_id UUID REFERENCES epics(id) ON DELETE SET NULL;
CREATE INDEX IF NOT EXISTS idx_work_items_epic ON work_items(epic_id);

-- A change's visibility is now a set of projects rather than one. An epic's log
-- row fans out to one row per linked project, so every member of every linked
-- project receives it; every other entity writes its single owning project.
-- project_id is deliberately not a foreign key, for the same reason the old
-- sync_log.project_id was not: a change-feed row must outlive the project it names.
CREATE TABLE IF NOT EXISTS sync_log_projects (
    log_id BIGINT NOT NULL REFERENCES sync_log(id) ON DELETE CASCADE,
    project_id UUID NOT NULL,
    PRIMARY KEY (log_id, project_id)
);
CREATE INDEX IF NOT EXISTS idx_sync_log_projects_project ON sync_log_projects(project_id, log_id);

-- The single project column is replaced, not kept beside the join: two answers
-- to "who may see this row" can disagree, and the epic is exactly the row where
-- they would.
DROP INDEX IF EXISTS idx_sync_log_project;
ALTER TABLE sync_log DROP COLUMN IF EXISTS project_id;
