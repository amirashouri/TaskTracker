-- Project repository metadata, project-scoped agents, single-use pairing codes,
-- and idempotent dispatches.
--
-- repo_url is additive and stays off the project JSON until the client that
-- declares it with a default ships: an installed client leaves
-- ignoreUnknownKeys off, so an added key is a decode failure. The column is
-- still written and read, because the agent brief needs it.
--
-- agent_runners.project_id is nullable on purpose. A NULL runner is a legacy
-- machine-wide registration and keeps claiming across its owner's projects; a
-- project-scoped runner created from a pairing code sees only its project.
--
-- SQLite has no ADD COLUMN IF NOT EXISTS; the migration runner skips the
-- "duplicate column name" error when this re-runs over an adopted schema.
ALTER TABLE projects ADD COLUMN repo_url TEXT NOT NULL DEFAULT '';
ALTER TABLE agent_runners ADD COLUMN project_id TEXT REFERENCES projects(id) ON DELETE SET NULL;
ALTER TABLE agent_runs ADD COLUMN idempotency_key TEXT NOT NULL DEFAULT '';

CREATE TABLE IF NOT EXISTS agent_pairing_codes (
	code_hash TEXT PRIMARY KEY,
	project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
	created_by TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	expires_at BIGINT NOT NULL,
	used_at BIGINT,
	created_at BIGINT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_agent_pairing_codes_expires ON agent_pairing_codes (expires_at);
CREATE UNIQUE INDEX IF NOT EXISTS idx_agent_runs_idempotent
	ON agent_runs (work_item_id, requested_by, idempotency_key)
	WHERE idempotency_key <> '';
