-- Code scanning: an opt-in, scheduled build of the instance's security
-- job per repository, whose SARIF results the runner uploads. Its own
-- schedule rather than a row in build_schedules, which a push replaces
-- wholesale from .gitbay/ci.yml.
CREATE TABLE security_scans (
    repo_id      INTEGER PRIMARY KEY REFERENCES repos(id) ON DELETE CASCADE,
    next_run     TEXT NOT NULL DEFAULT '',
    last_results TEXT NOT NULL DEFAULT '',
    issue_number INTEGER NOT NULL DEFAULT 0
);

-- What each repo was last told about. The fingerprint leaves the line
-- out, so a finding that moves is the same finding.
CREATE TABLE security_findings (
    repo_id     INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
    fingerprint TEXT NOT NULL,
    tool        TEXT NOT NULL,
    rule        TEXT NOT NULL,
    level       TEXT NOT NULL DEFAULT '',
    path        TEXT NOT NULL,
    line        INTEGER NOT NULL DEFAULT 0,
    message     TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (repo_id, fingerprint)
);
