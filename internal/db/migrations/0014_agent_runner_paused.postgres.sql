-- 0014_agent_runner_paused.postgres.sql
-- A reversible stop for a paired agent machine.
--
-- `revoked_at` retires a machine for good and invalidates its token, so it
-- cannot be used for "stop for now": a start has to restore the same machine
-- without re-pairing it. `paused_at` is that state. A paused runner may not
-- claim a run, and pausing also flags its live run for cancel so the worker
-- stops at its next heartbeat.
--
-- The process itself is not this server's to control: `tt agent run` executes
-- on the customer's machine. A paused row is the only stop the server can
-- honestly implement, and the worker observes it on its next claim.

ALTER TABLE agent_runners ADD COLUMN IF NOT EXISTS paused_at BIGINT;
