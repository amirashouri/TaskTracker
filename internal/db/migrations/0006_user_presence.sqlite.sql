-- Self-profile presence, distinct from the account lifecycle held in `status`.
--
-- `status` answers "may this account sign in?" (active/inactive/deleted) and is
-- admin-owned; `presence` answers "is this person available?" and is written by
-- the signed-in user through PATCH /v1/me. They are separate columns so a
-- presence picker can never inactivate an account.
--
-- SQLite does not support ADD COLUMN IF NOT EXISTS, but the migration runner
-- records each version so these statements only run once per database.

ALTER TABLE users ADD COLUMN presence TEXT NOT NULL DEFAULT 'active';
ALTER TABLE users ADD COLUMN presence_note TEXT NOT NULL DEFAULT '';
