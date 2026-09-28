-- When the key stops authenticating; NULL for never.
ALTER TABLE ssh_keys ADD COLUMN expires_at TEXT;
