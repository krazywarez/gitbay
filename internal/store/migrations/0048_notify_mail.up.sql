-- Whether activity notifications reach the account by mail as well as
-- the inbox. Off leaves inbox rows untouched and skips the mail half
-- only; login links and verification mail are not activity (#194).
ALTER TABLE users ADD COLUMN notify_mail INTEGER NOT NULL DEFAULT 1;
