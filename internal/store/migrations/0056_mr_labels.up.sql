-- Labels on merge requests, carried the same way issues carry them
-- (#231). The label rows themselves are shared: repo or org scoped.
--
-- labels now has two child tables, issue_labels and mr_labels: a future
-- rebuild of labels must carry both. With foreign keys on, rebuilding a
-- parent table drops its children's rows, which is what the first-line
-- "-- foreign_keys: off" directive in 0052 is for.
CREATE TABLE mr_labels (
    mr_id    INTEGER NOT NULL REFERENCES merge_requests(id) ON DELETE CASCADE,
    label_id INTEGER NOT NULL REFERENCES labels(id) ON DELETE CASCADE,
    PRIMARY KEY (mr_id, label_id)
);
