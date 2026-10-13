-- Initial schema. Time columns are epoch milliseconds (BIGINT).
-- Statements stay idempotent so a database built by the pre-runner inline DDL
-- upgrades in place when this version is recorded.

CREATE TABLE IF NOT EXISTS users (
	id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
	email TEXT UNIQUE NOT NULL,
	display_name TEXT,
	password_hash TEXT NOT NULL,
	position TEXT,
	avatar_id TEXT,
	status TEXT,
	last_seen BIGINT,
	created_at BIGINT
);

CREATE TABLE IF NOT EXISTS projects (
	id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
	key_prefix TEXT NOT NULL,
	name TEXT NOT NULL,
	details TEXT DEFAULT '',
	is_archived BOOLEAN DEFAULT FALSE,
	next_item_number INT DEFAULT 1,
	created_at BIGINT,
	updated_at BIGINT,
	version TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS sprints (
	id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
	project_id UUID REFERENCES projects(id) ON DELETE CASCADE,
	name TEXT NOT NULL,
	goal_story_points INT DEFAULT 0,
	goal TEXT,
	start_date BIGINT NOT NULL,
	end_date BIGINT NOT NULL,
	owner_id UUID REFERENCES users(id) ON DELETE SET NULL,
	is_finished BOOLEAN DEFAULT FALSE,
	finished_at BIGINT,
	is_archived BOOLEAN DEFAULT FALSE,
	created_at BIGINT,
	updated_at BIGINT,
	version TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS project_members (
	project_id UUID REFERENCES projects(id) ON DELETE CASCADE,
	user_id UUID REFERENCES users(id) ON DELETE CASCADE,
	role TEXT DEFAULT 'member',
	PRIMARY KEY (project_id, user_id)
);

CREATE TABLE IF NOT EXISTS work_items (
	id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
	project_id UUID REFERENCES projects(id) ON DELETE CASCADE,
	key TEXT NOT NULL,
	title TEXT NOT NULL,
	details TEXT DEFAULT '',
	status TEXT NOT NULL CHECK (status IN ('backlog','todo','in_progress','in_review','done','closed')),
	priority TEXT NOT NULL CHECK (priority IN ('lowest','low','medium','high','critical')),
	assignee_id UUID REFERENCES users(id) ON DELETE SET NULL,
	due_date BIGINT,
	recurrence JSONB,
	story_points INT,
	blocked_by_ids JSONB,
	sprint_id UUID REFERENCES sprints(id) ON DELETE SET NULL,
	rank INT DEFAULT 0,
	resolved_at BIGINT,
	created_at BIGINT,
	updated_at BIGINT,
	version TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS comments (
	id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
	work_item_id UUID REFERENCES work_items(id) ON DELETE CASCADE,
	author_id UUID REFERENCES users(id) ON DELETE CASCADE,
	body TEXT NOT NULL,
	parent_id UUID REFERENCES comments(id) ON DELETE CASCADE,
	created_at BIGINT,
	version TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS sync_log (
	id BIGSERIAL PRIMARY KEY,
	entity TEXT NOT NULL,
	entity_id UUID NOT NULL,
	operation TEXT NOT NULL CHECK (operation IN ('upsert','delete')),
	payload JSONB NOT NULL,
	version TEXT NOT NULL,
	user_id UUID REFERENCES users(id) ON DELETE SET NULL,
	created_at BIGINT
);

CREATE INDEX IF NOT EXISTS idx_sync_log_cursor ON sync_log (entity, created_at, id);
CREATE INDEX IF NOT EXISTS idx_work_items_project ON work_items (project_id);
CREATE INDEX IF NOT EXISTS idx_work_items_status ON work_items (status);
CREATE INDEX IF NOT EXISTS idx_work_items_sprint ON work_items (sprint_id);
CREATE INDEX IF NOT EXISTS idx_comments_work_item ON comments (work_item_id);
CREATE INDEX IF NOT EXISTS idx_sprints_project ON sprints (project_id);
