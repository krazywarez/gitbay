-- A finding a repository dismissed, by fingerprint, so it stays dismissed
-- across scans and line moves. Kept while scanning is off: turning it back
-- on does not bring back what was decided. by_id is NULL once that
-- account is gone.
CREATE TABLE security_dismissals (
    repo_id     INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
    fingerprint TEXT NOT NULL,
    reason      TEXT NOT NULL,
    by_id       INTEGER REFERENCES users(id) ON DELETE SET NULL,
    created_at  TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    PRIMARY KEY (repo_id, fingerprint)
);

-- What the last scan set aside rather than reported (codescan.Aside, as
-- JSON), so a change in it rewrites the issue.
ALTER TABLE security_scans ADD COLUMN aside TEXT NOT NULL DEFAULT '';
