-- Labels on merge requests, carried the same way issues carry them
-- (#231). The label rows themselves are shared: repo or org scoped.
CREATE TABLE mr_labels (
    mr_id    INTEGER NOT NULL REFERENCES merge_requests(id) ON DELETE CASCADE,
    label_id INTEGER NOT NULL REFERENCES labels(id) ON DELETE CASCADE,
    PRIMARY KEY (mr_id, label_id)
);
