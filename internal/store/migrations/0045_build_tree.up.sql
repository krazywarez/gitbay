-- The tree a build's commit points at. A rebase gives a commit a new sha
-- and the same tree; a job's result is a property of the tree, so a
-- success recorded against it stands for the new commit too (#177).
-- Empty for scheduled and tag builds, which are not deduplicated.
ALTER TABLE builds ADD COLUMN tree TEXT NOT NULL DEFAULT '';
CREATE INDEX builds_by_tree ON builds (repo_id, tree, job);
