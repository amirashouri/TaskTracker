-- Runner host kind, per-dispatch runner targeting, and the agent's result diff.
--
-- host is empty for a legacy registration or a pairing code issued by an older
-- client; the app treats that as "not stated", not as a fourth host kind.
--
-- requested_runner_id is the machine the dispatcher chose. NULL keeps the
-- pre-targeting behaviour: any machine that may claim the item's project can.
--
-- result_diff is captured by the runner from its workspace and is additive on
-- the run response; the client declares it with a default.
ALTER TABLE agent_runners ADD COLUMN host TEXT NOT NULL DEFAULT '';
ALTER TABLE agent_pairing_codes ADD COLUMN host TEXT NOT NULL DEFAULT '';
ALTER TABLE agent_runs ADD COLUMN requested_runner_id TEXT;
ALTER TABLE agent_runs ADD COLUMN result_diff TEXT NOT NULL DEFAULT '';
