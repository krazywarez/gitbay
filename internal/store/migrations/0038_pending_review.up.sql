-- A review composed as a unit and submitted at once. A diff comment used
-- to post the moment it was written, so a reviewer reading a change had
-- to either publish half-formed thoughts one at a time or keep them
-- somewhere else until they were done (#111).
--
-- pending marks a comment belonging to a review its author has not
-- submitted yet: visible to them alone, and published by `mr review`
-- along with the verdict. Default 0, so every comment that already
-- exists is published, which is what it was.
ALTER TABLE mr_diff_comments ADD COLUMN pending INTEGER NOT NULL DEFAULT 0;
-- The unresolved-thread merge gate and every listing filter on this, per
-- viewer, and one reviewer's pending set is small against the table.
CREATE INDEX mr_diff_comments_pending ON mr_diff_comments(mr_id, author_id, pending);
