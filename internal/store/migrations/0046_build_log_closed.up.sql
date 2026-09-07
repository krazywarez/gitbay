-- When a build's log stream from the runner ended. A runner reports a
-- build's outcome right after closing the stream; a stream that closed
-- with no outcome following means the runner is gone, and the build can
-- be failed within minutes instead of at the deadline (#179).
ALTER TABLE builds ADD COLUMN log_closed_at TEXT NOT NULL DEFAULT '';
