CREATE TABLE users (
    id         INTEGER PRIMARY KEY,
    username   TEXT NOT NULL UNIQUE,
    is_admin   INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);

CREATE TABLE emails (
    id          INTEGER PRIMARY KEY,
    user_id     INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    address     TEXT NOT NULL UNIQUE,
    verified_at TEXT,
    verified_by TEXT CHECK (verified_by IN ('smtp','admin')),
    is_primary  INTEGER NOT NULL DEFAULT 0,
    CHECK ((verified_at IS NULL) = (verified_by IS NULL))
);
CREATE INDEX emails_user ON emails(user_id);

CREATE TABLE ssh_keys (
    id           INTEGER PRIMARY KEY,
    user_id      INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    fingerprint  TEXT NOT NULL UNIQUE,
    algo         TEXT NOT NULL,
    blob         BLOB NOT NULL,
    scope        TEXT NOT NULL DEFAULT 'full',
    created_at   TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    last_used_at TEXT
);
CREATE INDEX ssh_keys_user ON ssh_keys(user_id);

CREATE TABLE pgp_keys (
    id          INTEGER PRIMARY KEY,
    user_id     INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    fingerprint TEXT NOT NULL UNIQUE,
    armored     TEXT NOT NULL,
    uids_json   TEXT NOT NULL DEFAULT '[]',
    expires_at  TEXT,
    revoked_at  TEXT,
    created_at  TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
CREATE INDEX pgp_keys_user ON pgp_keys(user_id);

CREATE TABLE orgs (
    id         INTEGER PRIMARY KEY,
    name       TEXT NOT NULL UNIQUE,
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);

CREATE TABLE org_members (
    org_id  INTEGER NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
    user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role    TEXT NOT NULL CHECK (role IN ('member','admin')),
    PRIMARY KEY (org_id, user_id)
);

CREATE TABLE repos (
    id             INTEGER PRIMARY KEY,
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
    UNIQUE (owner_kind, owner_id, name)
);

CREATE TABLE repo_access (
    repo_id      INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
    subject_kind TEXT NOT NULL CHECK (subject_kind IN ('user','org')),
    subject_id   INTEGER NOT NULL,
    role         TEXT NOT NULL CHECK (role IN ('read','write','admin')),
    PRIMARY KEY (repo_id, subject_kind, subject_id)
);

CREATE TABLE issues (
    id         INTEGER PRIMARY KEY,
    repo_id    INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
    number     INTEGER NOT NULL,
    author_id  INTEGER NOT NULL REFERENCES users(id),
    title      TEXT NOT NULL,
    body       TEXT NOT NULL DEFAULT '',
    state      TEXT NOT NULL DEFAULT 'open' CHECK (state IN ('open','closed')),
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    UNIQUE (repo_id, number)
);

CREATE TABLE issue_comments (
    id         INTEGER PRIMARY KEY,
    issue_id   INTEGER NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
    author_id  INTEGER NOT NULL REFERENCES users(id),
    body       TEXT NOT NULL,
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
CREATE INDEX issue_comments_issue ON issue_comments(issue_id);

CREATE TABLE labels (
    id      INTEGER PRIMARY KEY,
    repo_id INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
    name    TEXT NOT NULL,
    color   TEXT NOT NULL DEFAULT '',
    UNIQUE (repo_id, name)
);

CREATE TABLE issue_labels (
    issue_id INTEGER NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
    label_id INTEGER NOT NULL REFERENCES labels(id) ON DELETE CASCADE,
    PRIMARY KEY (issue_id, label_id)
);

CREATE TABLE issue_assignees (
    issue_id INTEGER NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
    user_id  INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    PRIMARY KEY (issue_id, user_id)
);

CREATE TABLE merge_requests (
    id             INTEGER PRIMARY KEY,
    repo_id        INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
    number         INTEGER NOT NULL,
    author_id      INTEGER NOT NULL REFERENCES users(id),
    source_repo_id INTEGER REFERENCES repos(id) ON DELETE SET NULL,
    source_ref     TEXT NOT NULL,
    target_ref     TEXT NOT NULL,
    title          TEXT NOT NULL,
    body           TEXT NOT NULL DEFAULT '',
    state          TEXT NOT NULL DEFAULT 'open'
                   CHECK (state IN ('open','merged','closed','source_gone')),
    head_sha       TEXT NOT NULL DEFAULT '',
    created_at     TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    updated_at     TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    UNIQUE (repo_id, number)
);

CREATE TABLE mr_comments (
    id         INTEGER PRIMARY KEY,
    mr_id      INTEGER NOT NULL REFERENCES merge_requests(id) ON DELETE CASCADE,
    author_id  INTEGER NOT NULL REFERENCES users(id),
    body       TEXT NOT NULL,
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
CREATE INDEX mr_comments_mr ON mr_comments(mr_id);

CREATE TABLE mr_reviews (
    id          INTEGER PRIMARY KEY,
    mr_id       INTEGER NOT NULL REFERENCES merge_requests(id) ON DELETE CASCADE,
    reviewer_id INTEGER NOT NULL REFERENCES users(id),
    verdict     TEXT NOT NULL CHECK (verdict IN ('approve','request_changes','comment')),
    head_sha    TEXT NOT NULL,
    stale       INTEGER NOT NULL DEFAULT 0,
    created_at  TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
CREATE INDEX mr_reviews_mr ON mr_reviews(mr_id);

CREATE TABLE commit_signatures (
    repo_id         INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
    commit_sha      TEXT NOT NULL,
    state           TEXT NOT NULL CHECK (state IN (
                        'verified','signed_unknown_key','signed_email_mismatch',
                        'signed_key_expired','signed_key_revoked',
                        'bad_signature','unsigned')),
    signer_user_id  INTEGER REFERENCES users(id) ON DELETE SET NULL,
    key_fingerprint TEXT,
    key_epoch       INTEGER NOT NULL,
    checked_at      TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    PRIMARY KEY (repo_id, commit_sha)
);
CREATE INDEX commit_signatures_fpr ON commit_signatures(key_fingerprint);

CREATE TABLE events (
    id         INTEGER PRIMARY KEY,
    repo_id    INTEGER REFERENCES repos(id) ON DELETE CASCADE,
    actor_id   INTEGER REFERENCES users(id) ON DELETE SET NULL,
    kind       TEXT NOT NULL,
    data_json  TEXT NOT NULL DEFAULT '{}',
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
CREATE INDEX events_repo ON events(repo_id, id);

CREATE TABLE audit_log (
    id         INTEGER PRIMARY KEY,
    actor_id   INTEGER REFERENCES users(id) ON DELETE SET NULL,
    action     TEXT NOT NULL,
    data_json  TEXT NOT NULL DEFAULT '{}',
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);

CREATE TABLE web_sessions (
    token_hash TEXT PRIMARY KEY,
    user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    expires_at TEXT NOT NULL
);

CREATE TABLE invites (
    code_hash  TEXT PRIMARY KEY,
    email      TEXT NOT NULL,
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    used_at    TEXT
);

CREATE TABLE settings (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
INSERT INTO settings (key, value) VALUES ('key_epoch', '1');
