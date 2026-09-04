-- The in-app notification inbox. The `notifications` table is the outbound
-- mail queue and keeps that job; this is the per-user list a client reads.
-- A row is a link plus enough text to decide whether to follow it, so the
-- list renders without touching the issue or merge request it points at.
CREATE TABLE inbox (
    id         INTEGER PRIMARY KEY,
    user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    repo_id    INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
    kind       TEXT NOT NULL,
    actor      TEXT NOT NULL,
    summary    TEXT NOT NULL,
    path       TEXT NOT NULL,
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    read_at    TEXT
);
-- The unread list and the badge count are the only reads, both newest
-- first per user.
CREATE INDEX inbox_unread ON inbox(user_id, read_at, id DESC);

-- Watching widens who hears about a repository beyond its owners and a
-- thread's participants; muting narrows it, and wins over both. Absence of
-- a row is the default: owners and participants, nobody else.
CREATE TABLE repo_watchers (
    repo_id INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
    user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    state   TEXT NOT NULL CHECK (state IN ('watching', 'muted')),
    PRIMARY KEY (repo_id, user_id)
);
