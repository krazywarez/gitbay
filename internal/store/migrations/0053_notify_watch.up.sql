-- Whether the account hears about every issue and merge request on the
-- repositories it can write to, as if it had run repo watch on each.
-- Consulted when a notice is delivered, not written on grant; an explicit
-- watch or mute row on the repository wins (#194).
ALTER TABLE users ADD COLUMN notify_watch INTEGER NOT NULL DEFAULT 0;
