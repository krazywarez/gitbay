CREATE TABLE import_markers (
    repo_id INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
    key     TEXT NOT NULL,
    value   TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (repo_id, key)
);
