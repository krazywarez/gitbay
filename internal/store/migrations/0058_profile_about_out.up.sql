-- The about text moves into profile/README.* in <owner>/.gitbay. A SQL
-- migration cannot write git objects, so the text is parked here and
-- `gitbayd admin migrate-profile-about` drains the table into
-- repositories. A later release drops the emptied table.
CREATE TABLE profile_about_backfill (
  owner_kind   TEXT    NOT NULL,
  owner_id     INTEGER NOT NULL,
  about        TEXT    NOT NULL,
  about_format TEXT    NOT NULL,
  PRIMARY KEY (owner_kind, owner_id)
);

INSERT INTO profile_about_backfill (owner_kind, owner_id, about, about_format)
SELECT 'user', id, about, about_format FROM users WHERE about <> '';

INSERT INTO profile_about_backfill (owner_kind, owner_id, about, about_format)
SELECT 'org', id, about, about_format FROM orgs WHERE about <> '';

ALTER TABLE users DROP COLUMN about;
ALTER TABLE users DROP COLUMN about_format;
ALTER TABLE orgs DROP COLUMN about;
ALTER TABLE orgs DROP COLUMN about_format;
