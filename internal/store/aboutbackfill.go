package store

// AboutRow is one owner's parked about text, waiting to become a file in
// <owner>/.gitbay. Migration 0058 fills the table; the backfill command
// drains it.
type AboutRow struct {
	OwnerKind string
	OwnerID   int64
	OwnerName string
	About     string
	Format    string
}

// PendingAboutBackfill lists the owners whose about text has not been
// written to a repository yet, resolving each one's name.
func (s *Store) PendingAboutBackfill() ([]AboutRow, error) {
	rows, err := s.DB.Query(`
		SELECT b.owner_kind, b.owner_id, b.about, b.about_format,
		       COALESCE(u.username, o.name)
		  FROM profile_about_backfill b
		  LEFT JOIN users u ON b.owner_kind = 'user' AND u.id = b.owner_id
		  LEFT JOIN orgs  o ON b.owner_kind = 'org'  AND o.id = b.owner_id
		 ORDER BY b.owner_kind, b.owner_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AboutRow
	for rows.Next() {
		var r AboutRow
		var name *string
		if err := rows.Scan(&r.OwnerKind, &r.OwnerID, &r.About, &r.Format, &name); err != nil {
			return nil, err
		}
		if name == nil {
			continue // the owner is gone; the row goes with them
		}
		r.OwnerName = *name
		out = append(out, r)
	}
	return out, rows.Err()
}

// ClearAboutBackfill drops one owner's row once its file exists.
func (s *Store) ClearAboutBackfill(kind string, id int64) error {
	_, err := s.DB.Exec(
		"DELETE FROM profile_about_backfill WHERE owner_kind = ? AND owner_id = ?", kind, id)
	return err
}
