ALTER TABLE issues ADD COLUMN body_format TEXT NOT NULL DEFAULT 'md';
ALTER TABLE issue_comments ADD COLUMN body_format TEXT NOT NULL DEFAULT 'md';
ALTER TABLE merge_requests ADD COLUMN body_format TEXT NOT NULL DEFAULT 'md';
ALTER TABLE mr_comments ADD COLUMN body_format TEXT NOT NULL DEFAULT 'md';
ALTER TABLE releases ADD COLUMN notes_format TEXT NOT NULL DEFAULT 'md';
