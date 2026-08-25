-- Teams scope repository access inside an organization. The pre-teams
-- model (every member writes every org repo) survives as the default via
-- orgs.members_role = 'write'; large orgs set it to 'read' or 'none' and
-- grant through teams instead.
ALTER TABLE orgs ADD COLUMN members_role TEXT NOT NULL DEFAULT 'write'
    CHECK (members_role IN ('write', 'read', 'none'));

CREATE TABLE teams (
    id     INTEGER PRIMARY KEY,
    org_id INTEGER NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
    name   TEXT NOT NULL,
    UNIQUE (org_id, name)
);
CREATE TABLE team_members (
    team_id INTEGER NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
    user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    PRIMARY KEY (team_id, user_id)
);
CREATE TABLE team_repos (
    team_id INTEGER NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
    repo_id INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
    role    TEXT NOT NULL CHECK (role IN ('read', 'write', 'admin')),
    PRIMARY KEY (team_id, repo_id)
);
CREATE INDEX team_repos_repo ON team_repos(repo_id);
