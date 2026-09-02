-- Per-account overrides of limits.max_repos_per_user and
-- max_bytes_per_user. NULL means the configured default.
ALTER TABLE users ADD COLUMN repo_limit INTEGER;
ALTER TABLE users ADD COLUMN byte_limit INTEGER;
