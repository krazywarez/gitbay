-- One row per runner account: when it last polled, the repositories it
-- asked to be limited to, and the build it currently holds.
CREATE TABLE runner_seen (
    user_id   INTEGER PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    last_seen TEXT NOT NULL,
    scope     TEXT NOT NULL DEFAULT '',
    build_id  INTEGER REFERENCES builds(id) ON DELETE SET NULL
);
