-- foreign_keys: off
-- Ids that are named after their row is gone are never handed out again
-- (#306). Without AUTOINCREMENT SQLite gives a new row MAX(id)+1, so
-- deleting the newest account, organization, repository, key or token
-- let the next one take its id, and with it whatever still named that
-- id: a deploy key's scope, a repo_access grant, a signed LFS or
-- reply-by-mail token, a hook's environment. webhook_deliveries and
-- push_queue rows are named by id by a sender that is mid-request when
-- a cascade can remove them.
--
-- Each table is rebuilt the way 0052 rebuilds labels: foreign keys off
-- for the step, legacy_alter_table so the children keep naming the
-- table through the rename and bind to the new one, and the runner's
-- foreign_key_check before commit. Rows keep their ids; indexes and
-- triggers are recreated. sqlite_sequence starts at the highest id in
-- the table or named anywhere else, so an id already freed and still
-- named (a deploy key for a deleted repository, a grant, an audit row or
-- a parked profile about text for a deleted owner) is not handed out
-- either.
PRAGMA legacy_alter_table = ON;

DROP TRIGGER users_owning_repos;
DROP TRIGGER orgs_owning_repos;

ALTER TABLE users RENAME TO users_old;
CREATE TABLE users (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    username     TEXT NOT NULL UNIQUE,
    is_admin     INTEGER NOT NULL DEFAULT 0,
    created_at   TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    pending      INTEGER NOT NULL DEFAULT 0,
    description  TEXT NOT NULL DEFAULT '',
    website      TEXT NOT NULL DEFAULT '',
    disabled     INTEGER NOT NULL DEFAULT 0,
    links        TEXT NOT NULL DEFAULT '',
    repo_limit   INTEGER,
    byte_limit   INTEGER,
    notify_mail  INTEGER NOT NULL DEFAULT 1,
    notify_watch INTEGER NOT NULL DEFAULT 0,
    theme        TEXT NOT NULL DEFAULT 'system',
    notify_push  INTEGER NOT NULL DEFAULT 1,
    diff_layout  TEXT NOT NULL DEFAULT 'unified',
    notify_reply INTEGER NOT NULL DEFAULT 0
);
INSERT INTO users (id, username, is_admin, created_at, pending, description, website, disabled,
        links, repo_limit, byte_limit, notify_mail, notify_watch, theme, notify_push, diff_layout, notify_reply)
    SELECT id, username, is_admin, created_at, pending, description, website, disabled,
        links, repo_limit, byte_limit, notify_mail, notify_watch, theme, notify_push, diff_layout, notify_reply
    FROM users_old;
DROP TABLE users_old;

ALTER TABLE orgs RENAME TO orgs_old;
CREATE TABLE orgs (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    name         TEXT NOT NULL UNIQUE,
    created_at   TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    description  TEXT NOT NULL DEFAULT '',
    website      TEXT NOT NULL DEFAULT '',
    members_role TEXT NOT NULL DEFAULT 'write'
        CHECK (members_role IN ('write', 'read', 'none')),
    links        TEXT NOT NULL DEFAULT ''
);
INSERT INTO orgs (id, name, created_at, description, website, members_role, links)
    SELECT id, name, created_at, description, website, members_role, links FROM orgs_old;
DROP TABLE orgs_old;

ALTER TABLE repos RENAME TO repos_old;
CREATE TABLE repos (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    owner_kind     TEXT NOT NULL CHECK (owner_kind IN ('user','org')),
    owner_id       INTEGER NOT NULL,
    name           TEXT NOT NULL,
    visibility     TEXT NOT NULL CHECK (visibility IN ('public','private')),
    default_branch TEXT NOT NULL DEFAULT 'main',
    fork_of        INTEGER REFERENCES repos(id) ON DELETE SET NULL,
    issue_counter  INTEGER NOT NULL DEFAULT 0,
    mr_counter     INTEGER NOT NULL DEFAULT 0,
    settings_json  TEXT NOT NULL DEFAULT '{}',
    created_at     TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    build_counter  INTEGER NOT NULL DEFAULT 0,
    UNIQUE (owner_kind, owner_id, name)
);
INSERT INTO repos (id, owner_kind, owner_id, name, visibility, default_branch, fork_of,
        issue_counter, mr_counter, settings_json, created_at, build_counter)
    SELECT id, owner_kind, owner_id, name, visibility, default_branch, fork_of,
        issue_counter, mr_counter, settings_json, created_at, build_counter
    FROM repos_old;
DROP TABLE repos_old;

ALTER TABLE api_tokens RENAME TO api_tokens_old;
CREATE TABLE api_tokens (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id          INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name             TEXT NOT NULL,
    token_hash       TEXT NOT NULL UNIQUE,
    scope            TEXT NOT NULL DEFAULT 'full' CHECK (scope IN ('full','read')),
    created_at       TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    expires_at       TEXT,
    last_used_at     TEXT,
    created_by_token INTEGER REFERENCES api_tokens(id) ON DELETE SET NULL,
    UNIQUE (user_id, name)
);
INSERT INTO api_tokens (id, user_id, name, token_hash, scope, created_at, expires_at,
        last_used_at, created_by_token)
    SELECT id, user_id, name, token_hash, scope, created_at, expires_at,
        last_used_at, created_by_token
    FROM api_tokens_old;
DROP TABLE api_tokens_old;

ALTER TABLE ssh_keys RENAME TO ssh_keys_old;
CREATE TABLE ssh_keys (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id          INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    fingerprint      TEXT NOT NULL UNIQUE,
    algo             TEXT NOT NULL,
    blob             BLOB NOT NULL,
    scope            TEXT NOT NULL DEFAULT 'full',
    created_at       TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    last_used_at     TEXT,
    label            TEXT NOT NULL DEFAULT '',
    created_by_token INTEGER REFERENCES api_tokens(id) ON DELETE SET NULL,
    expires_at       TEXT
);
INSERT INTO ssh_keys (id, user_id, fingerprint, algo, blob, scope, created_at, last_used_at,
        label, created_by_token, expires_at)
    SELECT id, user_id, fingerprint, algo, blob, scope, created_at, last_used_at,
        label, created_by_token, expires_at
    FROM ssh_keys_old;
DROP TABLE ssh_keys_old;
CREATE INDEX ssh_keys_user ON ssh_keys(user_id);

ALTER TABLE webhook_deliveries RENAME TO webhook_deliveries_old;
CREATE TABLE webhook_deliveries (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    webhook_id      INTEGER NOT NULL REFERENCES webhooks(id) ON DELETE CASCADE,
    event_id        INTEGER NOT NULL REFERENCES events(id) ON DELETE CASCADE,
    attempts        INTEGER NOT NULL DEFAULT 0,
    next_attempt_at TEXT,
    delivered_at    TEXT,
    failed_at       TEXT,
    last_status     INTEGER,
    last_error      TEXT,
    created_at      TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
INSERT INTO webhook_deliveries (id, webhook_id, event_id, attempts, next_attempt_at,
        delivered_at, failed_at, last_status, last_error, created_at)
    SELECT id, webhook_id, event_id, attempts, next_attempt_at,
        delivered_at, failed_at, last_status, last_error, created_at
    FROM webhook_deliveries_old;
DROP TABLE webhook_deliveries_old;
CREATE INDEX webhook_deliveries_due ON webhook_deliveries(next_attempt_at)
    WHERE delivered_at IS NULL AND failed_at IS NULL;

ALTER TABLE push_queue RENAME TO push_queue_old;
CREATE TABLE push_queue (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
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
INSERT INTO push_queue (id, device_id, title, body, path, attempts, next_attempt_at,
        sent_at, failed_at, last_error, created_at)
    SELECT id, device_id, title, body, path, attempts, next_attempt_at,
        sent_at, failed_at, last_error, created_at
    FROM push_queue_old;
DROP TABLE push_queue_old;
CREATE INDEX push_queue_due ON push_queue(next_attempt_at)
    WHERE sent_at IS NULL AND failed_at IS NULL;

CREATE TRIGGER users_owning_repos BEFORE DELETE ON users
WHEN EXISTS (SELECT 1 FROM repos WHERE owner_kind = 'user' AND owner_id = OLD.id)
BEGIN
    SELECT RAISE(ABORT, 'user still owns repositories');
END;
CREATE TRIGGER orgs_owning_repos BEFORE DELETE ON orgs
WHEN EXISTS (SELECT 1 FROM repos WHERE owner_kind = 'org' AND owner_id = OLD.id)
BEGIN
    SELECT RAISE(ABORT, 'organization still owns repositories');
END;

PRAGMA legacy_alter_table = OFF;

-- The copies above set each sequence to the table's own highest id; an
-- empty table has no row yet.
INSERT INTO sqlite_sequence (name, seq)
    SELECT t.name, 0 FROM (SELECT 'users' AS name UNION ALL SELECT 'orgs' UNION ALL SELECT 'repos'
        UNION ALL SELECT 'api_tokens' UNION ALL SELECT 'ssh_keys'
        UNION ALL SELECT 'webhook_deliveries' UNION ALL SELECT 'push_queue') t
    WHERE NOT EXISTS (SELECT 1 FROM sqlite_sequence s WHERE s.name = t.name);

UPDATE sqlite_sequence SET seq = MAX(seq,
        (SELECT COALESCE(MAX(subject_id), 0) FROM repo_access WHERE subject_kind = 'user'),
        (SELECT COALESCE(MAX(actor_ref), 0) FROM audit_log),
        (SELECT COALESCE(MAX(user_id), 0) FROM page_domains),
        (SELECT COALESCE(MAX(owner_id), 0) FROM profile_about_backfill WHERE owner_kind = 'user'))
    WHERE name = 'users';
UPDATE sqlite_sequence SET seq = MAX(seq,
        (SELECT COALESCE(MAX(subject_id), 0) FROM repo_access WHERE subject_kind = 'org'),
        (SELECT COALESCE(MAX(owner_id), 0) FROM profile_about_backfill WHERE owner_kind = 'org'))
    WHERE name = 'orgs';
UPDATE sqlite_sequence SET seq = MAX(seq,
        (SELECT COALESCE(MAX(CAST(substr(scope, 8, instr(substr(scope, 8), ':') - 1) AS INTEGER)), 0)
         FROM ssh_keys WHERE scope LIKE 'deploy:%'))
    WHERE name = 'repos';
