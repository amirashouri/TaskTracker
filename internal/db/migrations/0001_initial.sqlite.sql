-- Initial schema for SQLite: the same shape as 0001_initial.postgres.sql with
-- SQLite's type names. Referential actions are kept; SQLite supports them.

CREATE TABLE IF NOT EXISTS users (
	id TEXT PRIMARY KEY,
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
	id TEXT PRIMARY KEY,
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
	id TEXT PRIMARY KEY,
	project_id TEXT REFERENCES projects(id) ON DELETE CASCADE,
	name TEXT NOT NULL,
	goal_story_points INT DEFAULT 0,
	goal TEXT,
	start_date BIGINT NOT NULL,
	end_date BIGINT NOT NULL,
	owner_id TEXT REFERENCES users(id) ON DELETE SET NULL,
	is_finished BOOLEAN DEFAULT FALSE,
	finished_at BIGINT,
	is_archived BOOLEAN DEFAULT FALSE,
	created_at BIGINT,
	updated_at BIGINT,
	version TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS project_members (
	project_id TEXT REFERENCES projects(id) ON DELETE CASCADE,
	user_id TEXT REFERENCES users(id) ON DELETE CASCADE,
	role TEXT DEFAULT 'member',
	PRIMARY KEY (project_id, user_id)
);

CREATE TABLE IF NOT EXISTS work_items (
	id TEXT PRIMARY KEY,
	project_id TEXT REFERENCES projects(id) ON DELETE CASCADE,
	key TEXT NOT NULL,
	title TEXT NOT NULL,
	details TEXT DEFAULT '',
	status TEXT NOT NULL CHECK (status IN ('backlog','todo','in_progress','in_review','done','closed')),
	priority TEXT NOT NULL CHECK (priority IN ('lowest','low','medium','high','critical')),
	assignee_id TEXT REFERENCES users(id) ON DELETE SET NULL,
	due_date BIGINT,
	recurrence TEXT,
	story_points INT,
	blocked_by_ids TEXT,
	sprint_id TEXT REFERENCES sprints(id) ON DELETE SET NULL,
	rank INT DEFAULT 0,
	resolved_at BIGINT,
	created_at BIGINT,
	updated_at BIGINT,
	version TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS comments (
	id TEXT PRIMARY KEY,
	work_item_id TEXT REFERENCES work_items(id) ON DELETE CASCADE,
	author_id TEXT REFERENCES users(id) ON DELETE CASCADE,
	body TEXT NOT NULL,
	parent_id TEXT REFERENCES comments(id) ON DELETE CASCADE,
	created_at BIGINT,
	version TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS sync_log (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	entity TEXT NOT NULL,
	entity_id TEXT NOT NULL,
	operation TEXT NOT NULL CHECK (operation IN ('upsert','delete')),
	payload TEXT NOT NULL,
	version TEXT NOT NULL,
	user_id TEXT REFERENCES users(id) ON DELETE SET NULL,
	created_at BIGINT
);

CREATE INDEX IF NOT EXISTS idx_sync_log_cursor ON sync_log (entity, created_at, id);
CREATE INDEX IF NOT EXISTS idx_work_items_project ON work_items (project_id);
CREATE INDEX IF NOT EXISTS idx_work_items_status ON work_items (status);
CREATE INDEX IF NOT EXISTS idx_work_items_sprint ON work_items (sprint_id);
CREATE INDEX IF NOT EXISTS idx_comments_work_item ON comments (work_item_id);
CREATE INDEX IF NOT EXISTS idx_sprints_project ON sprints (project_id);
