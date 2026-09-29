-- Grants and parked profile about texts of deleted accounts and
-- organizations, and deploy keys of deleted repositories, name their
-- subject by id with no foreign key; deletes left them behind until #306. The counts go in a note the
-- daemon logs with the migration and then drops.
INSERT INTO settings (key, value)
    SELECT 'migration_note', 'removed grants of deleted accounts or organizations: ' || g.n
        || '; deploy keys of deleted repositories: ' || k.n
        || '; profile about texts of deleted accounts or organizations: ' || b.n
    FROM (SELECT COUNT(*) AS n FROM repo_access a
          WHERE (a.subject_kind = 'user' AND NOT EXISTS (SELECT 1 FROM users u WHERE u.id = a.subject_id))
             OR (a.subject_kind = 'org' AND NOT EXISTS (SELECT 1 FROM orgs o WHERE o.id = a.subject_id))) g,
         (SELECT COUNT(*) AS n FROM ssh_keys
          WHERE scope LIKE 'deploy:%' AND CAST(substr(scope, 8, instr(substr(scope, 8), ':') - 1) AS INTEGER)
              NOT IN (SELECT id FROM repos)) k,
         (SELECT COUNT(*) AS n FROM profile_about_backfill p
          WHERE (p.owner_kind = 'user' AND NOT EXISTS (SELECT 1 FROM users u WHERE u.id = p.owner_id))
             OR (p.owner_kind = 'org' AND NOT EXISTS (SELECT 1 FROM orgs o WHERE o.id = p.owner_id))) b
    WHERE g.n + k.n + b.n > 0
    ON CONFLICT (key) DO UPDATE SET value = excluded.value;

DELETE FROM repo_access
    WHERE (subject_kind = 'user' AND NOT EXISTS (SELECT 1 FROM users u WHERE u.id = repo_access.subject_id))
       OR (subject_kind = 'org' AND NOT EXISTS (SELECT 1 FROM orgs o WHERE o.id = repo_access.subject_id));

UPDATE settings SET value = value + 1
    WHERE key = 'key_epoch' AND EXISTS (SELECT 1 FROM ssh_keys
        WHERE scope LIKE 'deploy:%' AND CAST(substr(scope, 8, instr(substr(scope, 8), ':') - 1) AS INTEGER)
            NOT IN (SELECT id FROM repos));

DELETE FROM ssh_keys
    WHERE scope LIKE 'deploy:%' AND CAST(substr(scope, 8, instr(substr(scope, 8), ':') - 1) AS INTEGER)
        NOT IN (SELECT id FROM repos);

DELETE FROM profile_about_backfill
    WHERE (owner_kind = 'user' AND NOT EXISTS (SELECT 1 FROM users u WHERE u.id = profile_about_backfill.owner_id))
       OR (owner_kind = 'org' AND NOT EXISTS (SELECT 1 FROM orgs o WHERE o.id = profile_about_backfill.owner_id));
