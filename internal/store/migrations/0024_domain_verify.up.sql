-- DNS-challenge verification for custom pages domains. Existing claims
-- predate verification and stay serving: they are grandfathered verified.
ALTER TABLE page_domains ADD COLUMN token TEXT NOT NULL DEFAULT '';
ALTER TABLE page_domains ADD COLUMN user_id INTEGER NOT NULL DEFAULT 0;
ALTER TABLE page_domains ADD COLUMN verified_at TEXT NOT NULL DEFAULT '';
UPDATE page_domains SET verified_at = created_at WHERE verified_at = '';
