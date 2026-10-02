-- Quotas for organizations: limits.max_orgs_per_user counts orgs by the
-- account that created them; max_repos_per_org and max_bytes_per_org cap
-- each org, with per-org overrides. NULL means the configured default.
-- created_by carries no foreign key: ids are never reused (#306), and an
-- org outlives the account that made it.
ALTER TABLE orgs ADD COLUMN created_by INTEGER;
ALTER TABLE orgs ADD COLUMN repo_limit INTEGER;
ALTER TABLE orgs ADD COLUMN byte_limit INTEGER;
ALTER TABLE users ADD COLUMN org_limit INTEGER;
UPDATE orgs SET created_by = (
    SELECT m.user_id FROM org_members m
    WHERE m.org_id = orgs.id AND m.role = 'admin'
    ORDER BY m.rowid LIMIT 1);
CREATE INDEX orgs_created_by ON orgs(created_by);
