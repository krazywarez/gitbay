-- Where a failed build stopped: the 1-based step, 0 when it stopped
-- before any step or did not fail, and the runner's one-line reason.
ALTER TABLE builds ADD COLUMN failed_step INTEGER NOT NULL DEFAULT 0;
ALTER TABLE builds ADD COLUMN failed_reason TEXT NOT NULL DEFAULT '';
