package store

// Label is one of a repository's issue labels with its colour, "" when
// none was set (the web then derives one from the name), and how many
// issues carry it.
type Label struct {
	Name   string `json:"name"`
	Color  string `json:"color,omitempty"`
	Issues int64  `json:"issues"`
}

// ListLabels lists a repository's labels by name.
func (s *Store) ListLabels(repoID int64) ([]Label, error) {
	rows, err := s.DB.Query(`SELECT l.name, l.color, COUNT(il.issue_id)
		FROM labels l LEFT JOIN issue_labels il ON il.label_id = l.id
		WHERE l.repo_id = ? GROUP BY l.id ORDER BY l.name`, repoID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Label
	for rows.Next() {
		var l Label
		if err := rows.Scan(&l.Name, &l.Color, &l.Issues); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// SetLabel creates the label or sets its colour.
func (s *Store) SetLabel(repoID int64, name, color string) error {
	_, err := s.DB.Exec(`INSERT INTO labels (repo_id, name, color) VALUES (?, ?, ?)
		ON CONFLICT (repo_id, name) DO UPDATE SET color = excluded.color`, repoID, name, color)
	return err
}

// DeleteLabel removes a label and takes it off every issue.
func (s *Store) DeleteLabel(repoID int64, name string) error {
	res, err := s.DB.Exec("DELETE FROM labels WHERE repo_id = ? AND name = ?", repoID, name)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
