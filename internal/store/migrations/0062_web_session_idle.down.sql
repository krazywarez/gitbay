UPDATE web_sessions SET expires_at = absolute_expires_at;
ALTER TABLE web_sessions DROP COLUMN last_used_at;
ALTER TABLE web_sessions DROP COLUMN absolute_expires_at;
