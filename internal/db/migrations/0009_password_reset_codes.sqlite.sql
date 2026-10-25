-- 0009_password_reset_codes.sqlite.sql
-- Time-limited, single-use reset codes for the change-/forgot-password flow
-- (specs/t8.1-change-password.md). The types mirror the postgres file with
-- SQLite's names: timestamps are epoch milliseconds, as everywhere else in
-- this schema, so the Go side reads one representation from either database.
--
-- The row is the server's only copy of the code: there is no mail
-- infrastructure (AGENTS.md §7), so the admin endpoint returns the code to the
-- caller, who hands it to the member out of band.
--
-- SQLite does not support ADD COLUMN IF NOT EXISTS, but the migration runner
-- records each version so these statements only run once per database.

CREATE TABLE IF NOT EXISTS password_reset_codes (
	id TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
	code       TEXT NOT NULL,
	user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	expires_at BIGINT NOT NULL,
	consumed   BOOLEAN NOT NULL DEFAULT FALSE,
	created_at BIGINT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_password_reset_codes_code_user ON password_reset_codes(code, user_id);
CREATE INDEX IF NOT EXISTS idx_password_reset_codes_expires ON password_reset_codes(expires_at);
