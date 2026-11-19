-- Sprint membership uses a client-minted composite id (`sprintID:userID`) so a
-- re-push is an update on every device. The sync log is the one shared table
-- whose Postgres baseline still typed entity_id as UUID; widen it to TEXT so
-- the change feed can replay the same id the client sent. Existing UUID values
-- cast unchanged.
ALTER TABLE sync_log ALTER COLUMN entity_id TYPE TEXT USING entity_id::TEXT;
