CREATE TABLE issue_commit_refs (
    issue_id INTEGER NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
    sha      TEXT NOT NULL,
    PRIMARY KEY (issue_id, sha)
);
