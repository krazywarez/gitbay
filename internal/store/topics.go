package store

func (s *Store) ListTopics(repoID int64) ([]string, error) {
	rows, err := s.DB.Query("SELECT topic FROM repo_topics WHERE repo_id = ? ORDER BY topic", repoID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// AddTopic is idempotent: adding an existing topic is not an error.
func (s *Store) AddTopic(repoID int64, topic string) error {
	_, err := s.DB.Exec(
		"INSERT INTO repo_topics (repo_id, topic) VALUES (?, ?) ON CONFLICT DO NOTHING",
		repoID, topic)
	return err
}

func (s *Store) RemoveTopic(repoID int64, topic string) error {
	res, err := s.DB.Exec(
		"DELETE FROM repo_topics WHERE repo_id = ? AND topic = ?", repoID, topic)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
