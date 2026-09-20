-- Apple devices an account has registered, and the queue of pushes bound
-- for them. The mail queue's table is named `notifications`, so this one
-- cannot be; the columns mirror it so the drainer is the mailer's loop.
CREATE TABLE push_devices (
    id           INTEGER PRIMARY KEY,
    user_id      INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token        TEXT NOT NULL UNIQUE,
    label        TEXT NOT NULL DEFAULT '',
    created_at   TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    last_seen_at TEXT
);
CREATE INDEX push_devices_user ON push_devices(user_id);

CREATE TABLE push_queue (
    id              INTEGER PRIMARY KEY,
    device_id       INTEGER NOT NULL REFERENCES push_devices(id) ON DELETE CASCADE,
    title           TEXT NOT NULL,
    body            TEXT NOT NULL,
    path            TEXT NOT NULL,
    attempts        INTEGER NOT NULL DEFAULT 0,
    next_attempt_at TEXT,
    sent_at         TEXT,
    failed_at       TEXT,
    last_error      TEXT,
    created_at      TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
CREATE INDEX push_queue_due ON push_queue(next_attempt_at)
    WHERE sent_at IS NULL AND failed_at IS NULL;

-- Whether activity reaches the account's registered devices. Defaults on
-- and costs nothing for an account with no devices; it exists so a user
-- with a phone and an iPad silences both without deregistering each.
ALTER TABLE users ADD COLUMN notify_push INTEGER NOT NULL DEFAULT 1;
