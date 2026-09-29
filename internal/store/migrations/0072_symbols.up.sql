-- Symbol indexes (#293): the definitions found in the tree of a
-- repository's default branch head. tree is the key: a head whose tree
-- is the current index's needs no new one.
--
-- An index is written in chunks while its state is building, which no
-- read sees; publishing it makes it ok or partial (a bound was reached,
-- note says which) and retires the previous one in one short
-- transaction. Building and retired rows are deleted, symbols first and
-- in chunks, by the worker. At most one index per repository is current.
--
-- AUTOINCREMENT because a replaced index must not hand its id to the
-- next: a paging cursor names the index it was taken from.
CREATE TABLE symbol_indexes (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    repo_id    INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
    commit_sha TEXT NOT NULL,
    tree       TEXT NOT NULL,
    state      TEXT NOT NULL CHECK (state IN ('building', 'ok', 'partial', 'retired')),
    note       TEXT NOT NULL DEFAULT '',
    files      INTEGER NOT NULL DEFAULT 0,
    symbols    INTEGER NOT NULL DEFAULT 0,
    built_at   TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
CREATE INDEX symbol_indexes_repo ON symbol_indexes(repo_id, state);
CREATE UNIQUE INDEX symbol_indexes_current ON symbol_indexes(repo_id)
    WHERE state IN ('ok', 'partial');
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
-- The last run that could not build an index, kept apart so the current
-- index stays in place. Cleared by the next index published.
CREATE TABLE symbol_failures (
    repo_id   INTEGER PRIMARY KEY REFERENCES repos(id) ON DELETE CASCADE,
    tree      TEXT NOT NULL,
    note      TEXT NOT NULL,
    failed_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
-- Repositories waiting for the index worker. seq counts requests, so a
-- push that lands while a build runs leaves its request in place; force
-- rebuilds even when the tree is the indexed one. not_before defers a
-- retry after a failure; attempts counts those retries.
CREATE TABLE symbol_requests (
    repo_id      INTEGER PRIMARY KEY REFERENCES repos(id) ON DELETE CASCADE,
    seq          INTEGER NOT NULL DEFAULT 1,
    force        INTEGER NOT NULL DEFAULT 0,
    attempts     INTEGER NOT NULL DEFAULT 0,
    not_before   TEXT NOT NULL DEFAULT '',
    requested_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
