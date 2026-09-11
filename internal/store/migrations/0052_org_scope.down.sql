-- foreign_keys: off
-- Back to per-repository rows. An org-scoped row has no repository to go
-- to; the NOT NULL on repo_id refuses the copy, which fails the migration.
PRAGMA legacy_alter_table = ON;

ALTER TABLE labels RENAME TO labels_old;
CREATE TABLE labels (
    id      INTEGER PRIMARY KEY,
    repo_id INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
    name    TEXT NOT NULL,
    color   TEXT NOT NULL DEFAULT '',
    UNIQUE (repo_id, name)
);
INSERT INTO labels (id, repo_id, name, color)
    SELECT id, repo_id, name, color FROM labels_old;
DROP TABLE labels_old;

ALTER TABLE milestones RENAME TO milestones_old;
CREATE TABLE milestones (
    id          INTEGER PRIMARY KEY,
    repo_id     INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
    title       TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    due_date    TEXT NOT NULL DEFAULT '',
    state       TEXT NOT NULL DEFAULT 'open' CHECK (state IN ('open','closed')),
    created_at  TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    UNIQUE (repo_id, title)
);
INSERT INTO milestones (id, repo_id, title, description, due_date, state, created_at)
    SELECT id, repo_id, title, description, due_date, state, created_at FROM milestones_old;
DROP TABLE milestones_old;

PRAGMA legacy_alter_table = OFF;
