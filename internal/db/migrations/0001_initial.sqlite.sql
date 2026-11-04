-- The schema baseline for SQLite, mirroring 0001_initial.postgres.sql with
-- SQLite's type names.
--
-- Nothing had been released when the fourteen development versions were
-- squashed into this one, so there is no database in the field to upgrade: a
-- fresh install builds every table here. Post-release schema changes add the
-- next version for both drivers rather than editing this file.
--
-- Every statement is idempotent so a database built before the runner existed
-- is adopted on the next start, when 0001 is recorded again.

CREATE TABLE IF NOT EXISTS users (
	id TEXT PRIMARY KEY,
	email TEXT UNIQUE NOT NULL,
	display_name TEXT,
	password_hash TEXT NOT NULL,
	position TEXT,
	avatar_id TEXT,
	status TEXT,
	last_seen BIGINT,
	created_at BIGINT,
	role TEXT NOT NULL DEFAULT 'member',
	presence TEXT NOT NULL DEFAULT 'active',
	presence_note TEXT NOT NULL DEFAULT ''
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
	version TEXT NOT NULL DEFAULT '',
	tags TEXT,
	agent_enabled BOOLEAN NOT NULL DEFAULT FALSE
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
	version TEXT NOT NULL DEFAULT '',
	is_locked BOOLEAN NOT NULL DEFAULT FALSE
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
	version TEXT NOT NULL DEFAULT '',
	tag_ids TEXT
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

-- project_id is deliberately not a foreign key: a change-feed row must outlive
-- the project it names, or a client would never learn the project was deleted.
CREATE TABLE IF NOT EXISTS sync_log (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	entity TEXT NOT NULL,
	entity_id TEXT NOT NULL,
	operation TEXT NOT NULL CHECK (operation IN ('upsert','delete')),
	payload TEXT NOT NULL,
	version TEXT NOT NULL,
	user_id TEXT REFERENCES users(id) ON DELETE SET NULL,
	created_at BIGINT,
	project_id TEXT
);

-- Only the SHA-256 hash of a token's JWT id is stored. family_id groups every
-- token descended from one sign-in; reuse of a revoked member revokes the family.
CREATE TABLE IF NOT EXISTS refresh_tokens (
	token_hash TEXT PRIMARY KEY,
	family_id  TEXT NOT NULL,
	user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	issued_at  BIGINT NOT NULL,
	expires_at BIGINT NOT NULL,
	revoked_at BIGINT
);

-- Bytes live in the blob store on disk, never in a column. project_id is
-- denormalised, like sync_log.project_id, so an attachment outlives its project.
CREATE TABLE IF NOT EXISTS attachments (
	id TEXT PRIMARY KEY,
	work_item_id TEXT REFERENCES work_items(id) ON DELETE CASCADE,
	project_id TEXT NOT NULL,
	file_name TEXT NOT NULL,
	mime_type TEXT,
	size_bytes BIGINT NOT NULL,
	blob_ref TEXT,
	created_by TEXT REFERENCES users(id) ON DELETE SET NULL,
	created_at BIGINT NOT NULL,
	version TEXT NOT NULL DEFAULT ''
);

-- The server's only copy of a reset code: there is no mail infrastructure, so
-- the admin endpoint returns the code and the caller delivers it out of band.
CREATE TABLE IF NOT EXISTS password_reset_codes (
	id TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
	code       TEXT NOT NULL,
	user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	expires_at BIGINT NOT NULL,
	consumed   BOOLEAN NOT NULL DEFAULT FALSE,
	created_at BIGINT NOT NULL
);

-- A marker, not history: a member added after a device synced needs the project
-- replayed, because a cursor can never reach backwards. One row per
-- (project, member, feed) since each feed has its own cursor.
CREATE TABLE IF NOT EXISTS membership_grants (
	project_id TEXT REFERENCES projects(id) ON DELETE CASCADE,
	user_id TEXT REFERENCES users(id) ON DELETE CASCADE,
	scope TEXT NOT NULL,
	PRIMARY KEY (project_id, user_id, scope)
);

-- The agent itself runs as `tt agent run` on the machine holding the code. The
-- server owns only the durable queue, credential and progress log. No agent
-- state enters sync_log: a run is server-owned and non-mergeable.
CREATE TABLE IF NOT EXISTS agent_runners (
	id TEXT PRIMARY KEY,
	user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	name TEXT NOT NULL,
	token_hash TEXT NOT NULL UNIQUE,
	provider TEXT NOT NULL DEFAULT '',
	model_label TEXT NOT NULL DEFAULT '',
	last_seen_at BIGINT,
	revoked_at BIGINT,
	paused_at BIGINT,
	created_at BIGINT NOT NULL
);

CREATE TABLE IF NOT EXISTS agent_runs (
	id TEXT PRIMARY KEY,
	work_item_id TEXT NOT NULL REFERENCES work_items(id) ON DELETE CASCADE,
	project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
	requested_by TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	runner_id TEXT REFERENCES agent_runners(id) ON DELETE SET NULL,
	status TEXT NOT NULL CHECK (status IN ('queued','claimed','running','succeeded','failed','cancelled')),
	cancel_requested BOOLEAN NOT NULL DEFAULT FALSE,
	task_document TEXT NOT NULL,
	lease_expires_at BIGINT,
	summary TEXT NOT NULL DEFAULT '',
	error TEXT NOT NULL DEFAULT '',
	created_at BIGINT NOT NULL,
	started_at BIGINT,
	finished_at BIGINT
);

CREATE TABLE IF NOT EXISTS agent_run_events (
	run_id TEXT NOT NULL REFERENCES agent_runs(id) ON DELETE CASCADE,
	seq BIGINT NOT NULL,
	kind TEXT NOT NULL,
	message TEXT NOT NULL DEFAULT '',
	payload TEXT NOT NULL DEFAULT '',
	created_at BIGINT NOT NULL,
	PRIMARY KEY (run_id, seq)
);

CREATE INDEX IF NOT EXISTS idx_sync_log_cursor ON sync_log (entity, created_at, id);
CREATE INDEX IF NOT EXISTS idx_sync_log_project ON sync_log (project_id, id);
CREATE INDEX IF NOT EXISTS idx_work_items_project ON work_items (project_id);
CREATE INDEX IF NOT EXISTS idx_work_items_status ON work_items (status);
CREATE INDEX IF NOT EXISTS idx_work_items_sprint ON work_items (sprint_id);
CREATE INDEX IF NOT EXISTS idx_comments_work_item ON comments (work_item_id);
CREATE INDEX IF NOT EXISTS idx_sprints_project ON sprints (project_id);
CREATE INDEX IF NOT EXISTS idx_refresh_tokens_family ON refresh_tokens (family_id);
CREATE INDEX IF NOT EXISTS idx_refresh_tokens_user ON refresh_tokens (user_id);
CREATE INDEX IF NOT EXISTS idx_attachments_work_item ON attachments (work_item_id);
CREATE INDEX IF NOT EXISTS idx_attachments_project ON attachments (project_id);
CREATE INDEX IF NOT EXISTS idx_password_reset_codes_code_user ON password_reset_codes(code, user_id);
CREATE INDEX IF NOT EXISTS idx_password_reset_codes_expires ON password_reset_codes(expires_at);
CREATE INDEX IF NOT EXISTS idx_membership_grants_user ON membership_grants (user_id, scope);
CREATE INDEX IF NOT EXISTS idx_agent_runners_user ON agent_runners (user_id);
CREATE INDEX IF NOT EXISTS idx_agent_runs_item ON agent_runs (work_item_id, created_at);
CREATE INDEX IF NOT EXISTS idx_agent_runs_queue ON agent_runs (status, created_at);
CREATE UNIQUE INDEX IF NOT EXISTS idx_agent_runs_one_live
	ON agent_runs (work_item_id)
	WHERE status IN ('queued','claimed','running');
CREATE INDEX IF NOT EXISTS idx_agent_run_events_run ON agent_run_events (run_id, seq);
