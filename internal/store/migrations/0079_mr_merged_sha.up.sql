-- The commit a merge request landed as: the head itself for a
-- fast-forward, the new commit for merge, squash and rebase. Release
-- notes attribute merge requests to a commit range by it. Rows merged
-- before this column stay '' and are matched on head_sha.
ALTER TABLE merge_requests ADD COLUMN merged_sha TEXT NOT NULL DEFAULT '';
