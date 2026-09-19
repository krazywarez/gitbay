-- Which merge request, by number within the same repository, a closed
-- merge request was closed in favour of. NULL means none (#223).
ALTER TABLE merge_requests ADD COLUMN superseded_by INTEGER;
