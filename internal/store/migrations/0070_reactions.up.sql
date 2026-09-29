-- Reactions on issues, merge requests and their conversation comments
-- (#291). comment_id NULL is the reaction on the thread's own body. The
-- unique indexes are partial because NULLs never collide in a plain one.
CREATE TABLE issue_reactions (
    id         INTEGER PRIMARY KEY,
    issue_id   INTEGER NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
    comment_id INTEGER REFERENCES issue_comments(id) ON DELETE CASCADE,
    user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    reaction   TEXT NOT NULL
               CHECK (reaction IN ('+1','-1','laugh','hooray','confused','heart','rocket','eyes'))
);
CREATE UNIQUE INDEX issue_reactions_body ON issue_reactions(issue_id, user_id, reaction)
    WHERE comment_id IS NULL;
CREATE UNIQUE INDEX issue_reactions_comment ON issue_reactions(comment_id, user_id, reaction)
    WHERE comment_id IS NOT NULL;
CREATE INDEX issue_reactions_issue ON issue_reactions(issue_id);

CREATE TABLE mr_reactions (
    id         INTEGER PRIMARY KEY,
    mr_id      INTEGER NOT NULL REFERENCES merge_requests(id) ON DELETE CASCADE,
    comment_id INTEGER REFERENCES mr_comments(id) ON DELETE CASCADE,
    user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    reaction   TEXT NOT NULL
               CHECK (reaction IN ('+1','-1','laugh','hooray','confused','heart','rocket','eyes'))
);
CREATE UNIQUE INDEX mr_reactions_body ON mr_reactions(mr_id, user_id, reaction)
    WHERE comment_id IS NULL;
CREATE UNIQUE INDEX mr_reactions_comment ON mr_reactions(comment_id, user_id, reaction)
    WHERE comment_id IS NOT NULL;
CREATE INDEX mr_reactions_mr ON mr_reactions(mr_id);
