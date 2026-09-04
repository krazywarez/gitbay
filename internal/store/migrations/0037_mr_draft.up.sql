-- A merge request opened to show work in progress rather than to ask for
-- a merge. Deliberately a flag, not a state: state is open | merged |
-- closed | source_gone, and adding a fifth would change what every
-- `state = 'open'` query means, including the merge gates and the review
-- queue. A draft is open; it is just not asking yet (#111).
ALTER TABLE merge_requests ADD COLUMN draft INTEGER NOT NULL DEFAULT 0;
