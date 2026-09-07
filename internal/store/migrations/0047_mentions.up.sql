-- Who an issue or merge request body, or a comment on one, mentioned by
-- @name. A mentioned account is a participant of the thread from then
-- on, the way an author or commenter is (#202). repo_id carries the
-- cascade, since item_id is polymorphic.
CREATE TABLE mentions (
    repo_id INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
    kind    TEXT NOT NULL CHECK (kind IN ('issue', 'mr')),
    item_id INTEGER NOT NULL,
    user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    PRIMARY KEY (kind, item_id, user_id)
);
