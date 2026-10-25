-- An explicit lock on a sprint, replacing the old start-date rule.
--
-- The time-based refusal was a policy the client could not reverse: once a
-- sprint's start date arrived, its window and membership were frozen forever.
-- The owner asked for a lock they can apply and remove instead, so the state has
-- to travel with the sprint rather than be inferred from the clock.
--
-- Defaults false so every sprint created before this column keeps accepting
-- edits exactly as it did, and an old client that never sends the field keeps
-- writing an unlocked sprint.
--
-- SQLite does not support ADD COLUMN IF NOT EXISTS, but the migration runner
-- records each version so this statement only runs once per database.

ALTER TABLE sprints ADD COLUMN is_locked BOOLEAN NOT NULL DEFAULT FALSE;
