package store

// Bookmarks are public and counted; pins are private quick access. The
// two are deliberately separate tables rather than a flag on one, because
// they answer different questions: "what am I working on" and "what is
// worth coming back to" (#146).

func (s *Store) BookmarkRepo(userID, repoID int64) error {
	_, err := s.DB.Exec(
		"INSERT INTO repo_bookmarks (user_id, repo_id) VALUES (?, ?) ON CONFLICT DO NOTHING",
		userID, repoID)
	return err
}

func (s *Store) UnbookmarkRepo(userID, repoID int64) error {
	res, err := s.DB.Exec(
		"DELETE FROM repo_bookmarks WHERE user_id = ? AND repo_id = ?", userID, repoID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) IsBookmarked(userID, repoID int64) bool {
	var n int
	s.DB.QueryRow("SELECT COUNT(*) FROM repo_bookmarks WHERE user_id = ? AND repo_id = ?",
		userID, repoID).Scan(&n)
	return n > 0
}

// BookmarkCount is how many people have bookmarked a repository. It is
// public: the count is the point, and it names nobody.
func (s *Store) BookmarkCount(repoID int64) int {
	var n int
	s.DB.QueryRow("SELECT COUNT(*) FROM repo_bookmarks WHERE repo_id = ?", repoID).Scan(&n)
	return n
}

// ListBookmarks returns one user's bookmarked repositories, newest first.
// The caller filters by what the viewer may see: a bookmarked repository
// can since have gone private.
func (s *Store) ListBookmarks(userID int64) ([]Repo, error) {
	rows, err := s.DB.Query(repoSelect+`
		JOIN repo_bookmarks b ON b.repo_id = r.id
		WHERE b.user_id = ? ORDER BY b.bookmarked_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Repo
	for rows.Next() {
		r, err := scanRepo(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
