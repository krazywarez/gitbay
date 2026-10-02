DROP INDEX orgs_created_by;
ALTER TABLE users DROP COLUMN org_limit;
ALTER TABLE orgs DROP COLUMN byte_limit;
ALTER TABLE orgs DROP COLUMN repo_limit;
ALTER TABLE orgs DROP COLUMN created_by;
