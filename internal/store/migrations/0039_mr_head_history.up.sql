-- Every push to a merge request's source stales its reviews, and nothing
-- said what had changed between the two heads. Answering that needs the
-- head it used to be, which nothing kept: merge_requests.head_sha is
-- overwritten in place (#111).
--
-- One row per head a merge request has had, oldest first by id. The base
-- recorded alongside is the merge base at that moment, so a range-diff
-- compares like with like even when the target moved underneath.
CREATE TABLE mr_heads (
    id         INTEGER PRIMARY KEY,
    mr_id      INTEGER NOT NULL REFERENCES merge_requests(id) ON DELETE CASCADE,
    sha        TEXT NOT NULL,
    base_sha   TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
CREATE INDEX mr_heads_mr ON mr_heads(mr_id, id);

-- The current head of every existing merge request, so one that has never
-- been force-pushed since this migration still has a first entry to
-- measure from.
INSERT INTO mr_heads (mr_id, sha, base_sha)
SELECT id, head_sha, COALESCE(merged_base, '') FROM merge_requests WHERE head_sha <> '';
