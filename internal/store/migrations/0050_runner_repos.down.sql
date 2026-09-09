DROP TABLE runner_repos;
DROP TABLE runner_seen;
CREATE TABLE runner_seen (
    user_id   INTEGER PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    last_seen TEXT NOT NULL,
    scope     TEXT NOT NULL DEFAULT '',
    build_id  INTEGER REFERENCES builds(id) ON DELETE SET NULL
);
