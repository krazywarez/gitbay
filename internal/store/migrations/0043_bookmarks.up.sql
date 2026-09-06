-- Bookmarks are a public "saved for later", separate from pins: a pin is
-- private quick access to your own work, a bookmark says a repository is
-- worth coming back to and its count is a signal of that.
CREATE TABLE repo_bookmarks (
    user_id      INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    repo_id      INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
    bookmarked_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    PRIMARY KEY (user_id, repo_id)
);
CREATE INDEX repo_bookmarks_by_repo ON repo_bookmarks (repo_id);
