-- Dependency update checks: an opt-in per-repo sweep that compares the
-- manifests on the default branch against upstream registries and reports
-- what is behind in an issue. Opt-in because checking a private repo tells
-- a public registry what it depends on.
CREATE TABLE dep_checks (
    repo_id      INTEGER PRIMARY KEY REFERENCES repos(id) ON DELETE CASCADE,
    last_check   TEXT NOT NULL DEFAULT '',
    last_error   TEXT NOT NULL DEFAULT '',
    issue_number INTEGER NOT NULL DEFAULT 0
);

-- What each repo was last told about, so a repo that stays behind is
-- reported once rather than every sweep.
CREATE TABLE dep_reports (
    repo_id   INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
    ecosystem TEXT NOT NULL,
    name      TEXT NOT NULL,
    current   TEXT NOT NULL,
    latest    TEXT NOT NULL,
    PRIMARY KEY (repo_id, ecosystem, name)
);

-- The account dependency issues are authored by. Keyless and mailless: it
-- authors, it never authenticates.
--
-- The name was not reserved before this migration, so an instance upgrading
-- from v1.0.x may already have a user or an org holding it. Claim it only if
-- it is free: a daemon that will not start is a far worse outcome than a
-- dependency check that reports why it cannot open an issue.
INSERT INTO users (username, is_admin)
SELECT 'gitbay-bot', 0
WHERE NOT EXISTS (SELECT 1 FROM users WHERE username = 'gitbay-bot')
  AND NOT EXISTS (SELECT 1 FROM orgs WHERE name = 'gitbay-bot');
