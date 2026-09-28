-- Each row carries the hash of the row before it. actor_ref is the actor
-- id as written: actor_id is set to NULL when the account is deleted,
-- and the hash must not change with it. Rows written before this
-- migration keep an empty hash; the chain starts after them.
ALTER TABLE audit_log ADD COLUMN actor_ref INTEGER NOT NULL DEFAULT 0;
ALTER TABLE audit_log ADD COLUMN prev_hash TEXT NOT NULL DEFAULT '';
ALTER TABLE audit_log ADD COLUMN hash TEXT NOT NULL DEFAULT '';
UPDATE audit_log SET actor_ref = COALESCE(actor_id, 0);
