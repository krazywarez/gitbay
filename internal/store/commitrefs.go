package store

// TryRecordCommitRef marks a commit as having referenced an issue. It
// reports whether this pair was new — false means the reference was
// already processed and must not act again.
func (s *Store) TryRecordCommitRef(issueID int64, sha string) (bool, error) {
	res, err := s.DB.Exec(
		"INSERT INTO issue_commit_refs (issue_id, sha) VALUES (?, ?) ON CONFLICT DO NOTHING",
		issueID, sha)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}
