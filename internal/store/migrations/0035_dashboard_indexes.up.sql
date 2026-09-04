-- The dashboard's lists are "the newest N rows I can reach". Reachability
-- is a set of correlated subqueries, so no index can satisfy the filter;
-- what an index can do is supply the order, so the walk stops at LIMIT
-- instead of sorting every row in the table.
--
-- Deliberately not (state, updated_at): the merge request lists match
-- state with IN, which turns one ordered walk into two that must be
-- merged, and measured slower than no index at all — 12.1ms to 19.6ms on
-- 20k rows. Ordering alone is what these queries want.
CREATE INDEX issues_recent ON issues(updated_at DESC);
CREATE INDEX merge_requests_recent ON merge_requests(updated_at DESC);

-- issue_assignees' primary key leads with issue_id, so AssignedIssues had
-- no way in by user and tested EXISTS against every issue instead.
CREATE INDEX issue_assignees_user ON issue_assignees(user_id);
