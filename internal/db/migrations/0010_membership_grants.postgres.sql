-- A membership that was granted after a device had already synced needs the project's
-- history replayed: the change feed is `id > cursor`, and the rows a new member has to
-- see were logged before their cursor existed. A cursor can never reach backwards, so the
-- only correct answer is a resync — and the server needs to remember who is owed one.
--
-- This row is that marker, not a history. It is written when a member is added, read by
-- the pull (which answers `resyncRequired`), and deleted by the pull that starts from an
-- empty cursor — the replay that finally contains those rows.
--
-- One row per (project, member, feed) rather than one per member: each feed has its own
-- cursor, and a single marker consumed by the `projects` pull would leave `workItems` and
-- `attachments` stuck behind theirs with nothing left to say so.
CREATE TABLE IF NOT EXISTS membership_grants (
	project_id UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
	user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	scope TEXT NOT NULL,
	PRIMARY KEY (project_id, user_id, scope)
);

-- The pull asks by (user, scope) on every request, so that is the index that matters.
CREATE INDEX IF NOT EXISTS idx_membership_grants_user ON membership_grants (user_id, scope);
