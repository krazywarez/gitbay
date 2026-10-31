-- An attachment may name the jobs its runner claims in that repository,
-- comma-separated; '' is every job. It lets two runners share a
-- repository by job: one that can reach a geo-blocked source for the
-- repository's own job, another with the code scanning image.
ALTER TABLE runner_repos ADD COLUMN jobs TEXT NOT NULL DEFAULT '';
