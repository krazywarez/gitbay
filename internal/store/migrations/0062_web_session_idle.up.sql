-- expires_at slides forward on use, never past absolute_expires_at.
-- Sessions open now keep their cap and get a full idle window from here.
ALTER TABLE web_sessions ADD COLUMN absolute_expires_at TEXT;
ALTER TABLE web_sessions ADD COLUMN last_used_at TEXT;
UPDATE web_sessions SET
    absolute_expires_at = expires_at,
    last_used_at = strftime('%Y-%m-%dT%H:%M:%fZ','now'),
    expires_at = min(expires_at, strftime('%Y-%m-%dT%H:%M:%fZ','now','+12 hours'));
