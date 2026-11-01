-- An advisory a repository dismissed, by id, so it stays dismissed across
-- checks. Kept while checks are off, as code scanning dismissals are.
CREATE TABLE dep_advisory_dismissals (
    repo_id     INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
    advisory_id TEXT NOT NULL,
    reason      TEXT NOT NULL,
    by_id       INTEGER REFERENCES users(id) ON DELETE SET NULL,
    created_at  TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    PRIMARY KEY (repo_id, advisory_id)
);

-- A Go advisory naming packages none of the repository's Go files import.
-- Kept so status can say what was set aside, but not reported.
ALTER TABLE dep_advisories ADD COLUMN not_imported INTEGER NOT NULL DEFAULT 0;
