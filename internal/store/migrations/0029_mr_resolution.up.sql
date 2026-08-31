-- Who resolved a merge request and when. The state alone cannot say it:
-- updated_at moves for every edit, and the actor only survived in the
-- events table.
ALTER TABLE merge_requests ADD COLUMN merged_at TEXT NOT NULL DEFAULT '';
ALTER TABLE merge_requests ADD COLUMN merged_by INTEGER REFERENCES users(id) ON DELETE SET NULL;
ALTER TABLE merge_requests ADD COLUMN closed_at TEXT NOT NULL DEFAULT '';
ALTER TABLE merge_requests ADD COLUMN closed_by INTEGER REFERENCES users(id) ON DELETE SET NULL;

-- Backfill from the mr.merged events already recorded. json_extract is
-- available in the SQLite builds gitbay links; the payload is
-- {"number":N,"sha":"..."} written by the merge path.
UPDATE merge_requests SET
    merged_at = COALESCE((
        SELECT e.created_at FROM events e
        WHERE e.repo_id = merge_requests.repo_id AND e.kind = 'mr.merged'
          AND json_extract(e.data_json, '$.number') = merge_requests.number
        ORDER BY e.id DESC LIMIT 1), ''),
    merged_by = (
        SELECT e.actor_id FROM events e
        WHERE e.repo_id = merge_requests.repo_id AND e.kind = 'mr.merged'
          AND json_extract(e.data_json, '$.number') = merge_requests.number
        ORDER BY e.id DESC LIMIT 1)
WHERE state = 'merged';
