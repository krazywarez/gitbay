-- One row per receive-pack in flight. The hook names its push by the
-- token; hookd answers only a live one. Only the SHA-256 is stored.
CREATE TABLE push_tokens (
    token_hash TEXT PRIMARY KEY,
    repo_id    INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
    user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    scope      TEXT NOT NULL,
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    expires_at TEXT NOT NULL
);
