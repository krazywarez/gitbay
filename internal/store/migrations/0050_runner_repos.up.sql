-- A runner key is attached to the repositories it may claim builds for
-- (#184). runner_seen is rekeyed by key so two runners on one account
-- are two rows; what it held were heartbeats, so the rows are dropped.
CREATE TABLE runner_repos (
    key_id   INTEGER NOT NULL REFERENCES ssh_keys(id) ON DELETE CASCADE,
    repo_id  INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
    added_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    PRIMARY KEY (key_id, repo_id)
);
CREATE INDEX runner_repos_repo ON runner_repos(repo_id);

DROP TABLE runner_seen;
CREATE TABLE runner_seen (
    key_id    INTEGER PRIMARY KEY REFERENCES ssh_keys(id) ON DELETE CASCADE,
    user_id   INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    last_seen TEXT NOT NULL,
    scope     TEXT NOT NULL DEFAULT '',
    build_id  INTEGER REFERENCES builds(id) ON DELETE SET NULL
);
