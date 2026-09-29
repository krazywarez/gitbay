-- The diff layout the web UI renders for the account: unified or split
-- (#290).
ALTER TABLE users ADD COLUMN diff_layout TEXT NOT NULL DEFAULT 'unified';
