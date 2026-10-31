-- Known vulnerabilities in dependencies: the same opt-in sweep checks the
-- resolved dependency set against OSV and reports matches in an issue of
-- its own, so an advisory is not filed among routine updates.
ALTER TABLE dep_checks ADD COLUMN advisory_issue_number INTEGER NOT NULL DEFAULT 0;

-- What each repo was last told about, as dep_reports is for updates.
CREATE TABLE dep_advisories (
    repo_id     INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
    ecosystem   TEXT NOT NULL,
    name        TEXT NOT NULL,
    version     TEXT NOT NULL,
    advisory_id TEXT NOT NULL,
    summary     TEXT NOT NULL DEFAULT '',
    severity    TEXT NOT NULL DEFAULT '',
    fixed       TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (repo_id, ecosystem, name, version, advisory_id)
);
