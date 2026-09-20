ALTER TABLE users ADD COLUMN about TEXT NOT NULL DEFAULT '';
ALTER TABLE users ADD COLUMN about_format TEXT NOT NULL DEFAULT 'md';
ALTER TABLE orgs ADD COLUMN about TEXT NOT NULL DEFAULT '';
ALTER TABLE orgs ADD COLUMN about_format TEXT NOT NULL DEFAULT 'md';

UPDATE users SET
  about = (SELECT about FROM profile_about_backfill
            WHERE owner_kind = 'user' AND owner_id = users.id),
  about_format = (SELECT about_format FROM profile_about_backfill
            WHERE owner_kind = 'user' AND owner_id = users.id)
  WHERE id IN (SELECT owner_id FROM profile_about_backfill WHERE owner_kind = 'user');

UPDATE orgs SET
  about = (SELECT about FROM profile_about_backfill
            WHERE owner_kind = 'org' AND owner_id = orgs.id),
  about_format = (SELECT about_format FROM profile_about_backfill
            WHERE owner_kind = 'org' AND owner_id = orgs.id)
  WHERE id IN (SELECT owner_id FROM profile_about_backfill WHERE owner_kind = 'org');

DROP TABLE profile_about_backfill;
