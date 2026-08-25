-- CI secrets: per-repo values injected into build environments. Stored
-- like mirror tokens — server-side, set over stdin, never echoed back.
CREATE TABLE build_secrets (
    repo_id INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
    name    TEXT NOT NULL,
    value   TEXT NOT NULL,
    PRIMARY KEY (repo_id, name)
);
-- Scheduled builds: jobs with a cron expression, synced from .gitbay/ci.yml
-- on push and fired by the daemon's scheduler.
CREATE TABLE build_schedules (
    repo_id  INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
    job      TEXT NOT NULL,
    cron     TEXT NOT NULL,
    next_run TEXT NOT NULL,
    PRIMARY KEY (repo_id, job)
);
