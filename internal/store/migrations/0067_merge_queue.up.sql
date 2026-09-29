-- A merge request queued with `mr merge --when-ready`: merged as user_id
-- with strategy once its gates pass. reason is why the last attempt did
-- not merge. A merge request that is merged or closed leaves the queue,
-- whichever path did it.
--
-- credential is what the merge was queued with: 'key' (key_id) or
-- 'token' (token_id), or '' for a web session, which binds to the
-- account alone. A removed key or revoked token nulls its id, so the id
-- of a later credential never stands in for it.
CREATE TABLE mr_merge_queue (
    mr_id      INTEGER PRIMARY KEY REFERENCES merge_requests(id) ON DELETE CASCADE,
    user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    strategy   TEXT NOT NULL DEFAULT '',
    credential TEXT NOT NULL DEFAULT '' CHECK (credential IN ('', 'key', 'token')),
    key_id     INTEGER REFERENCES ssh_keys(id) ON DELETE SET NULL,
    token_id   INTEGER REFERENCES api_tokens(id) ON DELETE SET NULL,
    reason     TEXT NOT NULL DEFAULT '',
    queued_at  TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
CREATE INDEX mr_merge_queue_user ON mr_merge_queue(user_id);
CREATE TRIGGER mr_merge_queue_leave AFTER UPDATE OF state ON merge_requests
WHEN NEW.state IN ('merged', 'closed')
BEGIN
    DELETE FROM mr_merge_queue WHERE mr_id = NEW.id;
END;
