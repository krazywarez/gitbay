DROP INDEX issues_recent;
DROP INDEX merge_requests_recent;
CREATE INDEX issues_recent ON issues(updated_at DESC);
CREATE INDEX merge_requests_recent ON merge_requests(updated_at DESC);
