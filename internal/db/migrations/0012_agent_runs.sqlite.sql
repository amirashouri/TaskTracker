-- 0012_agent_runs.sqlite.sql
-- The queue, the runner credential, and the progress log behind
-- "send this work item to an agent".
--
-- The agent itself is not here. It runs as `tt agent run` on the machine that
-- holds the code, because this server cannot reach a customer's local model and
-- must not hold an LLM provider key. What the server owns is the durable part:
-- who may run work, which run a worker has taken, and what it has reported.
--
-- Three tables rather than one, because they have different lifetimes:
--
--   agent_runners           one row per paired machine; revocation is a date
--   agent_runs              one row per dispatch, and the run's whole state
--   agent_run_events        append-only, high volume, pruned on retention
--
-- Time columns are epoch milliseconds in a BIGINT, like every other table here;
-- internal/entities/timestamp.go is the only place that knows the wire format.
-- The API re-serves these as ISO-8601 whole seconds, matching entity payloads.
--
-- No agent state enters `sync_log`. A run is server-owned and non-mergeable,
-- and a device cannot have authored one while offline. Keeping it out is also
-- what keeps the frozen wire contract frozen: the client's `workItem` DTO gains
-- no field, so an installed build keeps decoding it.

CREATE TABLE IF NOT EXISTS agent_runners (
	id TEXT PRIMARY KEY,
	user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	name TEXT NOT NULL,
	-- The token is stored only as a hash and is returned exactly once, at
	-- registration. It is opaque rather than a JWT so revocation closes the
	-- door immediately instead of waiting out an expiry.
	token_hash TEXT NOT NULL UNIQUE,
	-- Advertised, display-only. The provider key and model choice live in the
	-- runner's own config on the developer's machine and never arrive here.
	provider TEXT NOT NULL DEFAULT '',
	model_label TEXT NOT NULL DEFAULT '',
	last_seen_at BIGINT,
	revoked_at BIGINT,
	created_at BIGINT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_agent_runners_user ON agent_runners (user_id);

CREATE TABLE IF NOT EXISTS agent_runs (
	id TEXT PRIMARY KEY,
	work_item_id TEXT NOT NULL REFERENCES work_items(id) ON DELETE CASCADE,
	-- Denormalised: claim scopes by project membership, and that check must not
	-- require loading the work item on every long-poll wakeup.
	project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
	requested_by TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	runner_id TEXT REFERENCES agent_runners(id) ON DELETE SET NULL,
	status TEXT NOT NULL CHECK (status IN ('queued','claimed','running','succeeded','failed','cancelled')),
	-- Cooperative cancel: the runner stops when it next observes this, which is
	-- why a run is never deleted to stop it.
	cancel_requested BOOLEAN NOT NULL DEFAULT FALSE,
	-- The task snapshot the runner receives, as the JSON document the dispatch
	-- built. Opaque and re-served verbatim: a runner older than the server still
	-- decodes what it is given, and the job never becomes typed columns that
	-- would later gain a field.
	task_document TEXT NOT NULL,
	-- A crashed runner must not strand its run, so a claim is a lease.
	lease_expires_at BIGINT,
	summary TEXT NOT NULL DEFAULT '',
	error TEXT NOT NULL DEFAULT '',
	created_at BIGINT NOT NULL,
	started_at BIGINT,
	finished_at BIGINT
);

CREATE INDEX IF NOT EXISTS idx_agent_runs_item ON agent_runs (work_item_id, created_at);
CREATE INDEX IF NOT EXISTS idx_agent_runs_queue ON agent_runs (status, created_at);

-- One live run per work item, enforced by the database rather than by a
-- read-then-write check in a handler: two devices can dispatch at the same
-- instant, and the loser of that race must be the one that is refused.
CREATE UNIQUE INDEX IF NOT EXISTS idx_agent_runs_one_live
	ON agent_runs (work_item_id)
	WHERE status IN ('queued','claimed','running');

CREATE TABLE IF NOT EXISTS agent_run_events (
	run_id TEXT NOT NULL REFERENCES agent_runs(id) ON DELETE CASCADE,
	-- Assigned by the server, monotonic per run. The client polls with
	-- `afterSeq`, so the pair is the cursor and the primary key.
	seq BIGINT NOT NULL,
	kind TEXT NOT NULL,
	message TEXT NOT NULL DEFAULT '',
	-- Opaque JSON string, so a new event kind or a new detail never changes a
	-- response key set the client pinned.
	payload TEXT NOT NULL DEFAULT '',
	created_at BIGINT NOT NULL,
	PRIMARY KEY (run_id, seq)
);

CREATE INDEX IF NOT EXISTS idx_agent_run_events_run ON agent_run_events (run_id, seq);
