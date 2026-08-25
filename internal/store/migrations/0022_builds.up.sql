ALTER TABLE repos ADD COLUMN build_counter INTEGER NOT NULL DEFAULT 0;
CREATE TABLE builds (
    id          INTEGER PRIMARY KEY,
    repo_id     INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
    number      INTEGER NOT NULL,
    job         TEXT NOT NULL,
    sha         TEXT NOT NULL,
    ref         TEXT NOT NULL,
    steps       TEXT NOT NULL, -- JSON array of shell commands
    status      TEXT NOT NULL DEFAULT 'pending', -- pending|running|success|failure
    log         BLOB NOT NULL DEFAULT x'',
    created_at  TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
    started_at  TEXT NOT NULL DEFAULT '',
    finished_at TEXT NOT NULL DEFAULT '',
    UNIQUE (repo_id, number)
);
CREATE INDEX builds_pending ON builds(status);
