CREATE TABLE commit_statuses (
    id          INTEGER PRIMARY KEY,
    repo_id     INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
    commit_sha  TEXT NOT NULL,
    context     TEXT NOT NULL,
    state       TEXT NOT NULL CHECK (state IN ('pending','success','failure','error')),
    description TEXT NOT NULL DEFAULT '',
    target_url  TEXT NOT NULL DEFAULT '',
    creator_id  INTEGER REFERENCES users(id) ON DELETE SET NULL,
    created_at  TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    updated_at  TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    UNIQUE (repo_id, commit_sha, context)
);
CREATE INDEX commit_statuses_sha ON commit_statuses(repo_id, commit_sha);
