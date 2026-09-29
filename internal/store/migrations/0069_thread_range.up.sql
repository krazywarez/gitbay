-- A review thread may anchor to a range of lines ending at line: a
-- suggestion replaces start_line through line. 0 means the thread is on
-- line alone, which every existing row is.
ALTER TABLE mr_diff_comments ADD COLUMN start_line INTEGER NOT NULL DEFAULT 0;
