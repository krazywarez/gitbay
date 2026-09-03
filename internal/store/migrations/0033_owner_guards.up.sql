-- repos.owner_id is polymorphic over users and orgs, so no foreign key
-- can hold it; DeleteUser and DeleteOrg refuse while repositories remain.
-- These triggers make that refusal structural: a direct or buggy delete
-- cannot orphan a repository either.
CREATE TRIGGER users_owning_repos BEFORE DELETE ON users
WHEN EXISTS (SELECT 1 FROM repos WHERE owner_kind = 'user' AND owner_id = OLD.id)
BEGIN
    SELECT RAISE(ABORT, 'user still owns repositories');
END;
CREATE TRIGGER orgs_owning_repos BEFORE DELETE ON orgs
WHEN EXISTS (SELECT 1 FROM repos WHERE owner_kind = 'org' AND owner_id = OLD.id)
BEGIN
    SELECT RAISE(ABORT, 'organization still owns repositories');
END;
