-- One row per member of a sprint, with the capacity they committed at planning.
-- The id is client-minted from the sprint and user ids so a re-push is an update.
CREATE TABLE IF NOT EXISTS sprint_members (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL,
    sprint_id TEXT NOT NULL,
    user_id TEXT NOT NULL,
    available_story_points INTEGER NOT NULL DEFAULT 0,
    created_at BIGINT,
    updated_at BIGINT,
    version TEXT NOT NULL,
    UNIQUE (sprint_id, user_id)
);
CREATE INDEX IF NOT EXISTS idx_sprint_members_sprint ON sprint_members(sprint_id);
