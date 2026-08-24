ALTER TABLE issues DROP COLUMN milestone_id;
ALTER TABLE merge_requests DROP COLUMN milestone_id;
DROP TABLE milestones;
