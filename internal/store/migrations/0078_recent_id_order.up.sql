-- The dashboard breaks updated_at ties by id, newest first. The 0035
-- indexes carry rowid ascending after updated_at, so that order would
-- sort the table instead of walking the index.
DROP INDEX issues_recent;
DROP INDEX merge_requests_recent;
CREATE INDEX issues_recent ON issues(updated_at DESC, id DESC);
CREATE INDEX merge_requests_recent ON merge_requests(updated_at DESC, id DESC);
