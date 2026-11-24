-- The SQLite spelling of 0009_epic_owner.postgres.sql; see that file for why.
ALTER TABLE epics ADD COLUMN owner_id TEXT REFERENCES users(id) ON DELETE SET NULL;

UPDATE epics
   SET owner_id = (
       SELECT id FROM users
        WHERE role = 'admin' AND COALESCE(status, '') <> 'deleted'
        ORDER BY created_at, id
        LIMIT 1
   )
 WHERE owner_id IS NULL;
