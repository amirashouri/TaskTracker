-- Store one attachment's index entry per work item. The bytes live in the blob
-- store (AttachmentRoot on disk), never in a column — a TEXT column would
-- serialise them into the sync payload, which is exactly the transport that must
-- not exist.
--
-- work_item_id cascades like comments do: deleting a work item removes its
-- attachments. The blob file itself is removed by the sync engine on an explicit
-- attachment delete; a work-item cascade may orphan the file, which costs disk,
-- never correctness.
--
-- project_id is denormalised (no foreign key, like sync_log.project_id) so an
-- attachment outlives the project it names — otherwise a client would never learn
-- the project was deleted. It carries the membership scope for the change feed
-- and for the file API's authz check.

CREATE TABLE IF NOT EXISTS attachments (
	id TEXT PRIMARY KEY,
	work_item_id TEXT REFERENCES work_items(id) ON DELETE CASCADE,
	project_id TEXT NOT NULL,
	file_name TEXT NOT NULL,
	mime_type TEXT,
	size_bytes BIGINT NOT NULL,
	blob_ref TEXT,
	created_by TEXT REFERENCES users(id) ON DELETE SET NULL,
	created_at BIGINT NOT NULL,
	version TEXT NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS idx_attachments_work_item ON attachments (work_item_id);
CREATE INDEX IF NOT EXISTS idx_attachments_project ON attachments (project_id);
