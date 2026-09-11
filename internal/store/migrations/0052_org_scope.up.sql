-- Labels and milestones scoped to a repository or to an org (#203).
-- Exactly one of repo_id and org_id is set. Uniqueness is per scope, as
-- two partial indexes; the app refuses a repo name the org already holds.
--
-- Both tables have children (issue_labels, issues.milestone_id,
-- merge_requests.milestone_id). Since SQLite 3.26 renaming a parent
-- rewrites the children's foreign keys to follow it, which would bind them
-- to the *_old tables. legacy_alter_table keeps the children naming labels
-- and milestones, which the new tables then are. foreign_keys stays on:
-- nothing references the *_old tables, so dropping them cascades nothing.
PRAGMA foreign_keys = OFF;
PRAGMA legacy_alter_table = ON;

ALTER TABLE labels RENAME TO labels_old;
CREATE TABLE labels (
    id      INTEGER PRIMARY KEY,
    repo_id INTEGER REFERENCES repos(id) ON DELETE CASCADE,
    org_id  INTEGER REFERENCES orgs(id) ON DELETE CASCADE,
    name    TEXT NOT NULL,
    color   TEXT NOT NULL DEFAULT '',
    CHECK ((repo_id IS NULL) <> (org_id IS NULL))
);
INSERT INTO labels (id, repo_id, name, color)
    SELECT id, repo_id, name, color FROM labels_old;
DROP TABLE labels_old;
CREATE UNIQUE INDEX labels_repo_name ON labels(repo_id, name) WHERE repo_id IS NOT NULL;
CREATE UNIQUE INDEX labels_org_name  ON labels(org_id, name)  WHERE org_id  IS NOT NULL;

ALTER TABLE milestones RENAME TO milestones_old;
CREATE TABLE milestones (
    id          INTEGER PRIMARY KEY,
    repo_id     INTEGER REFERENCES repos(id) ON DELETE CASCADE,
    org_id      INTEGER REFERENCES orgs(id) ON DELETE CASCADE,
    title       TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    due_date    TEXT NOT NULL DEFAULT '',
    state       TEXT NOT NULL DEFAULT 'open' CHECK (state IN ('open','closed')),
    created_at  TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    CHECK ((repo_id IS NULL) <> (org_id IS NULL))
);
INSERT INTO milestones (id, repo_id, title, description, due_date, state, created_at)
    SELECT id, repo_id, title, description, due_date, state, created_at FROM milestones_old;
DROP TABLE milestones_old;
CREATE UNIQUE INDEX milestones_repo_title ON milestones(repo_id, title) WHERE repo_id IS NOT NULL;
CREATE UNIQUE INDEX milestones_org_title  ON milestones(org_id, title)  WHERE org_id  IS NOT NULL;

PRAGMA legacy_alter_table = OFF;
PRAGMA foreign_keys = ON;
