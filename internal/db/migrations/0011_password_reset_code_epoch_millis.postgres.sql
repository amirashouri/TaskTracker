-- 0011_password_reset_code_epoch_millis.postgres.sql
-- Repair a password_reset_codes table whose timestamp columns are not epoch
-- milliseconds.
--
-- Why this exists. 0009 is committed with BIGINT columns, but at least one
-- database was migrated by an earlier draft of 0009 that declared `timestamp
-- with time zone`. The runner records a version once, so re-running 0009 is a
-- no-op there (`CREATE TABLE IF NOT EXISTS`) and the mismatch survives: every
-- insert of an int64 fails with
--
--   column "expires_at" is of type timestamp with time zone but expression
--   is of type bigint
--
-- and the admin reset-code endpoint answers 500 with no way to tell why.
--
-- Why the columns are dropped and re-added rather than cast. The USING
-- expression has to depend on the column's current type, and the runner splits
-- statements on semicolons without understanding dollar quoting
-- (internal/db/migrate.go, splitStatements), so a `DO $$ ... $$` guard is not
-- available. `ALTER ... TYPE BIGINT USING (extract(epoch FROM x) * 1000)`
-- repairs the drifted table and then fails on a correct one, because
-- `extract(epoch FROM bigint)` does not exist. Dropping the column and adding
-- it back is the one form that is valid whatever the column held, and it is
-- re-runnable, which is what a migration over a pre-runner database needs.
--
-- What it costs. The codes themselves survive; their timestamps do not, and a
-- code that reads `expires_at = 0` is expired. That is the right degradation
-- for a 24-hour single-use secret: the member is told the code expired, not
-- that it was never issued. A drifted install cannot have lost anything by
-- this, because the insert that would have written a code had always failed.
--
-- The Go side is unchanged: epoch milliseconds in an int64, as
-- internal/entities/timestamp.go settles for the whole schema.

ALTER TABLE password_reset_codes DROP COLUMN IF EXISTS expires_at;
ALTER TABLE password_reset_codes ADD COLUMN IF NOT EXISTS expires_at BIGINT NOT NULL DEFAULT 0;
ALTER TABLE password_reset_codes ALTER COLUMN expires_at DROP DEFAULT;

ALTER TABLE password_reset_codes DROP COLUMN IF EXISTS created_at;
ALTER TABLE password_reset_codes ADD COLUMN IF NOT EXISTS created_at BIGINT NOT NULL DEFAULT 0;
ALTER TABLE password_reset_codes ALTER COLUMN created_at DROP DEFAULT;

-- The index went with the column it covered.
CREATE INDEX IF NOT EXISTS idx_password_reset_codes_expires ON password_reset_codes(expires_at);
