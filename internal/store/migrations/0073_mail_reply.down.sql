DROP TABLE mail_replies;
ALTER TABLE notifications DROP COLUMN reply_to;
ALTER TABLE users DROP COLUMN notify_reply;
