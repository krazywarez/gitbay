ALTER TABLE issue_comments ADD COLUMN kind TEXT NOT NULL DEFAULT 'comment';
ALTER TABLE mr_comments ADD COLUMN kind TEXT NOT NULL DEFAULT 'comment';
