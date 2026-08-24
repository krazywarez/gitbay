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
ALTER TABLE issues ADD COLUMN milestone_id INTEGER REFERENCES milestones(id) ON DELETE SET NULL;
ALTER TABLE merge_requests ADD COLUMN milestone_id INTEGER REFERENCES milestones(id) ON DELETE SET NULL;
