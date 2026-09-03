-- A build from a merge request head fetched out of another repository
-- runs code the target's owners did not write; it gets no secrets.
ALTER TABLE builds ADD COLUMN trusted INTEGER NOT NULL DEFAULT 1;
