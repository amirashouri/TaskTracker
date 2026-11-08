-- Project repository metadata, project-scoped agents, single-use pairing codes,
-- and idempotent dispatches.
--
-- repo_url is additive project metadata for the agent brief, not a credential.
-- Project JSON emits it only when non-empty, and only because the client now
-- declares the key with a default: an older installed build leaves
-- ignoreUnknownKeys off, so it fails to decode a project that has a repo URL.
--
-- agent_runners.project_id is nullable on purpose. A NULL runner is a legacy
-- machine-wide registration and keeps claiming across its owner's projects; a
-- project-scoped runner created from a pairing code sees only its project.
ALTER TABLE projects ADD COLUMN IF NOT EXISTS repo_url TEXT NOT NULL DEFAULT '';
ALTER TABLE agent_runners ADD COLUMN IF NOT EXISTS project_id UUID REFERENCES projects(id) ON DELETE SET NULL;
ALTER TABLE agent_runs ADD COLUMN IF NOT EXISTS idempotency_key TEXT NOT NULL DEFAULT '';

CREATE TABLE IF NOT EXISTS agent_pairing_codes (
	code_hash TEXT PRIMARY KEY,
	project_id UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
	created_by UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	expires_at BIGINT NOT NULL,
	used_at BIGINT,
	created_at BIGINT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_agent_pairing_codes_expires ON agent_pairing_codes (expires_at);
CREATE UNIQUE INDEX IF NOT EXISTS idx_agent_runs_idempotent
	ON agent_runs (work_item_id, requested_by, idempotency_key)
	WHERE idempotency_key <> '';
