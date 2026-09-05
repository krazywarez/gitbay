-- Requesting a review is a direct push to a specific person, the merge
-- request counterpart of issue_assignees (0001).
CREATE TABLE mr_review_requests (
    mr_id   INTEGER NOT NULL REFERENCES merge_requests(id) ON DELETE CASCADE,
    user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    PRIMARY KEY (mr_id, user_id)
);

-- Same problem as issue_assignees (0035): the primary key leads with
-- mr_id, so the review queue had no way in by user.
CREATE INDEX mr_review_requests_user ON mr_review_requests(user_id);
