-- One symbol index per repository: the definitions found in the tree of
-- the default branch's head (#293). tree is the key: a head whose tree is
-- the indexed one needs no new index. state is ok, partial (a bound was
-- reached; note says which) or failed (note says why), and a failed or
-- partial index is kept as the record for its tree rather than retried.
-- AUTOINCREMENT because a replaced index must not hand its id to the
-- next: a paging cursor names the index it was taken from.
CREATE TABLE symbol_indexes (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    repo_id    INTEGER NOT NULL UNIQUE REFERENCES repos(id) ON DELETE CASCADE,
    commit_sha TEXT NOT NULL,
    tree       TEXT NOT NULL,
    state      TEXT NOT NULL CHECK (state IN ('ok', 'partial', 'failed')),
    note       TEXT NOT NULL DEFAULT '',
    files      INTEGER NOT NULL DEFAULT 0,
    symbols    INTEGER NOT NULL DEFAULT 0,
    built_at   TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
-- name is what is listed, key the name as written at a use site (they
-- differ for Go methods: Type.Method and Method). lname and lkey are
-- their lower-case forms, for case-insensitive prefix ranges.
CREATE TABLE symbols (
    id       INTEGER PRIMARY KEY,
    index_id INTEGER NOT NULL REFERENCES symbol_indexes(id) ON DELETE CASCADE,
    name     TEXT NOT NULL,
    lname    TEXT NOT NULL,
    key      TEXT NOT NULL,
    lkey     TEXT NOT NULL,
    kind     TEXT NOT NULL,
    path     TEXT NOT NULL,
    line     INTEGER NOT NULL
);
CREATE INDEX symbols_lname ON symbols(index_id, lname);
CREATE INDEX symbols_lkey ON symbols(index_id, lkey);
CREATE INDEX symbols_key ON symbols(index_id, key);
CREATE INDEX symbols_path ON symbols(index_id, path, line);
-- Repositories waiting for the index worker. seq counts requests, so a
-- push that lands while a build runs leaves its request in place; force
-- rebuilds even when the tree is the indexed one.
CREATE TABLE symbol_requests (
    repo_id      INTEGER PRIMARY KEY REFERENCES repos(id) ON DELETE CASCADE,
    seq          INTEGER NOT NULL DEFAULT 1,
    force        INTEGER NOT NULL DEFAULT 0,
    requested_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
