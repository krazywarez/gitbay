-- Custom domains for pages: one domain serves one repo's pages branch.
CREATE TABLE page_domains (
    domain     TEXT PRIMARY KEY,
    repo_id    INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now'))
);
CREATE INDEX page_domains_repo ON page_domains(repo_id);
