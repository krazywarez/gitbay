-- Reply by mail (#295). notify_reply puts a Reply-To carrying a reply
-- token on the account's issue and merge request mail when the instance
-- polls a mailbox for replies. notifications.reply_to is that address,
-- per queued message, blanked once it is sent. mail_replies records each
-- reply that posted a comment, keyed by account, thread and Message-ID
-- (or a hash of the message when it has none), so a message fetched
-- twice posts once.
ALTER TABLE users ADD COLUMN notify_reply INTEGER NOT NULL DEFAULT 0;
ALTER TABLE notifications ADD COLUMN reply_to TEXT NOT NULL DEFAULT '';
CREATE TABLE mail_replies (
    message_key TEXT PRIMARY KEY,
    created_at  TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
