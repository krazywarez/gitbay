CREATE TABLE mirrors (
    id         INTEGER PRIMARY KEY,
    repo_id    INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
    direction  TEXT NOT NULL CHECK (direction IN ('push','pull')),
    url        TEXT NOT NULL,
    username   TEXT NOT NULL DEFAULT '',
    token      TEXT NOT NULL DEFAULT '',
    dirty      INTEGER NOT NULL DEFAULT 1,
    last_sync  TEXT NOT NULL DEFAULT '',
    last_error TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    UNIQUE (repo_id, direction, url)
);
