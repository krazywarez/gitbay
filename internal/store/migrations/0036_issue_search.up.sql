-- Full-text search over issue and merge request prose. Finding an old
-- issue meant listing and scrolling, and the instance-wide `search`
-- matched titles only, because a LIKE with a leading wildcard cannot use
-- an index (#114).
--
-- External-content tables: the FTS index stores only the terms and points
-- at the row it came from, so the prose is not duplicated. content_rowid
-- ties a row to issues.id / merge_requests.id.
CREATE VIRTUAL TABLE issue_fts USING fts5(
    title, body,
    content = 'issues',
    content_rowid = 'id',
    tokenize = 'unicode61'
);
CREATE VIRTUAL TABLE mr_fts USING fts5(
    title, body,
    content = 'merge_requests',
    content_rowid = 'id',
    tokenize = 'unicode61'
);

-- An external-content table is not maintained for you: every write to the
-- base table has to be mirrored, and a delete or update must first insert
-- the old values under the 'delete' command or the index keeps terms for
-- prose that no longer exists.
CREATE TRIGGER issues_fts_insert AFTER INSERT ON issues BEGIN
    INSERT INTO issue_fts (rowid, title, body) VALUES (new.id, new.title, new.body);
END;
CREATE TRIGGER issues_fts_delete AFTER DELETE ON issues BEGIN
    INSERT INTO issue_fts (issue_fts, rowid, title, body) VALUES ('delete', old.id, old.title, old.body);
END;
CREATE TRIGGER issues_fts_update AFTER UPDATE OF title, body ON issues BEGIN
    INSERT INTO issue_fts (issue_fts, rowid, title, body) VALUES ('delete', old.id, old.title, old.body);
    INSERT INTO issue_fts (rowid, title, body) VALUES (new.id, new.title, new.body);
END;

CREATE TRIGGER mrs_fts_insert AFTER INSERT ON merge_requests BEGIN
    INSERT INTO mr_fts (rowid, title, body) VALUES (new.id, new.title, new.body);
END;
CREATE TRIGGER mrs_fts_delete AFTER DELETE ON merge_requests BEGIN
    INSERT INTO mr_fts (mr_fts, rowid, title, body) VALUES ('delete', old.id, old.title, old.body);
END;
CREATE TRIGGER mrs_fts_update AFTER UPDATE OF title, body ON merge_requests BEGIN
    INSERT INTO mr_fts (mr_fts, rowid, title, body) VALUES ('delete', old.id, old.title, old.body);
    INSERT INTO mr_fts (rowid, title, body) VALUES (new.id, new.title, new.body);
END;

-- Everything already in the database, since the triggers only see writes
-- from here on.
INSERT INTO issue_fts (rowid, title, body) SELECT id, title, body FROM issues;
INSERT INTO mr_fts (rowid, title, body) SELECT id, title, body FROM merge_requests;
