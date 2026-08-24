CREATE TABLE mr_diff_comments (
    id          INTEGER PRIMARY KEY,
    mr_id       INTEGER NOT NULL REFERENCES merge_requests(id) ON DELETE CASCADE,
    author_id   INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    head_sha    TEXT NOT NULL,
    path        TEXT NOT NULL,
    side        TEXT NOT NULL DEFAULT 'new' CHECK (side IN ('new','old')),
    line        INTEGER NOT NULL,
    body        TEXT NOT NULL,
    reply_to    INTEGER REFERENCES mr_diff_comments(id) ON DELETE CASCADE,
    resolved_at TEXT,
    resolved_by INTEGER REFERENCES users(id) ON DELETE SET NULL,
    created_at  TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
CREATE INDEX mr_diff_comments_mr ON mr_diff_comments(mr_id);
