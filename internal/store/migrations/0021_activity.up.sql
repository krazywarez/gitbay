-- Commits as an activity signal: recorded when they land on the default
-- branch, attributed by verified author email, deduped by sha so rebases
-- and re-pushes never double-count. day is the author date (YYYY-MM-DD),
-- so imported history keeps its real timeline.
CREATE TABLE commit_activity (
    repo_id INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
    sha     TEXT NOT NULL,
    user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    day     TEXT NOT NULL,
    PRIMARY KEY (repo_id, sha)
);
CREATE INDEX commit_activity_user ON commit_activity(user_id, day);
