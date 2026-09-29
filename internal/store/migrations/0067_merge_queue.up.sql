-- A merge request queued with `mr merge --when-ready`: merged as user_id
-- with strategy once its gates pass. reason is why the last attempt did
-- not merge. A merge request that is merged or closed leaves the queue,
-- whichever path did it.
CREATE TABLE mr_merge_queue (
    mr_id     INTEGER PRIMARY KEY REFERENCES merge_requests(id) ON DELETE CASCADE,
    user_id   INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    strategy  TEXT NOT NULL DEFAULT '',
    reason    TEXT NOT NULL DEFAULT '',
    queued_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
CREATE INDEX mr_merge_queue_user ON mr_merge_queue(user_id);
CREATE TRIGGER mr_merge_queue_leave AFTER UPDATE OF state ON merge_requests
WHEN NEW.state IN ('merged', 'closed')
BEGIN
    DELETE FROM mr_merge_queue WHERE mr_id = NEW.id;
END;
