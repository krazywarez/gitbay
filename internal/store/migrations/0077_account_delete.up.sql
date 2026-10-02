-- Self-service account deletion (#322). A request waits for its mailed
-- link in account_deletions; a confirmed one sets users.delete_after and
-- disables the account until the reaper purges it. users.ghost marks the
-- one account that takes over what deleted accounts wrote.
CREATE TABLE account_deletions (
    user_id    INTEGER PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    token_hash TEXT NOT NULL UNIQUE,
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    expires_at TEXT NOT NULL
);
ALTER TABLE users ADD COLUMN delete_after TEXT;
ALTER TABLE users ADD COLUMN ghost INTEGER NOT NULL DEFAULT 0;
