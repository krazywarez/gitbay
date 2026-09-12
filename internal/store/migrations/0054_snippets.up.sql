-- Snippets: named text files a user owns and shares by URL, outside any
-- repository. public_id is the opaque id in URLs and commands.
CREATE TABLE snippets (
    id          INTEGER PRIMARY KEY,
    public_id   TEXT NOT NULL UNIQUE,
    owner_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    description TEXT NOT NULL DEFAULT '',
    visibility  TEXT NOT NULL CHECK (visibility IN ('public','unlisted','private')),
    created_at  TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    updated_at  TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
CREATE INDEX snippets_owner ON snippets(owner_id, id);

CREATE TABLE snippet_files (
    snippet_id INTEGER NOT NULL REFERENCES snippets(id) ON DELETE CASCADE,
    name       TEXT NOT NULL,
    content    BLOB NOT NULL,
    size       INTEGER NOT NULL,
    PRIMARY KEY (snippet_id, name)
);
