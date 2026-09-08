-- When the scheduler failed a build instead of its runner reporting it,
-- so builds ended by the reaper can be counted (#184).
ALTER TABLE builds ADD COLUMN reaped_at TEXT NOT NULL DEFAULT '';
