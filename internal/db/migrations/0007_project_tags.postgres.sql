-- Project-scoped tags, plus the tag ids a work item carries.
--
-- Tags are project configuration and travel on the project row, so a client
-- learns the catalog from the project it already syncs. Both columns are JSON:
-- `tags` is an array of {id,name,color,icon}, `tag_ids` an array of ids. Work
-- items reference tags by id only; a deleted tag leaves stale ids behind, which
-- every reader drops when it cannot resolve them.

ALTER TABLE projects ADD COLUMN IF NOT EXISTS tags TEXT;
ALTER TABLE work_items ADD COLUMN IF NOT EXISTS tag_ids TEXT;
