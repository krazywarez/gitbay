package store

// DashboardItem is one open issue or MR row on the logged-in homepage.
type DashboardItem struct {
	RepoPath  string
	Number    int64
	Title     string
	Author    string
	State     string
	UpdatedAt string
}

// involvedRepos filters to repositories the user owns, is granted on, or
// reaches through org membership — or rows the user authored anywhere.
const involvedCond = `(
	x.author_id = ?1
	OR (r.owner_kind = 'user' AND r.owner_id = ?1)
	OR EXISTS (SELECT 1 FROM repo_access a
	           WHERE a.repo_id = r.id AND a.subject_kind = 'user' AND a.subject_id = ?1)
	OR EXISTS (SELECT 1 FROM org_members mm
	           JOIN orgs oo ON oo.id = mm.org_id
	           WHERE r.owner_kind = 'org' AND mm.org_id = r.owner_id AND mm.user_id = ?1
	             AND (mm.role = 'admin' OR oo.members_role <> 'none'))
	OR EXISTS (SELECT 1 FROM team_repos tr
	           JOIN team_members tm ON tm.team_id = tr.team_id AND tm.user_id = ?1
	           WHERE tr.repo_id = r.id)
)`

func (s *Store) dashboardQuery(q string, userID int64) ([]DashboardItem, error) {
	rows, err := s.DB.Query(q, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DashboardItem
	for rows.Next() {
		var d DashboardItem
		if err := rows.Scan(&d.RepoPath, &d.Number, &d.Title, &d.Author, &d.State, &d.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// DashboardMRs returns open merge requests involving the user: on their
// repositories (owned, granted, org) or authored by them anywhere.
func (s *Store) DashboardMRs(userID int64) ([]DashboardItem, error) {
	return s.dashboardQuery(`
		SELECT COALESCE(u.username, o.name) || '/' || r.name,
		       x.number, x.title, au.username, x.state, x.updated_at
		FROM merge_requests x
		JOIN repos r ON r.id = x.repo_id
		LEFT JOIN users u ON r.owner_kind = 'user' AND u.id = r.owner_id
		LEFT JOIN orgs o  ON r.owner_kind = 'org'  AND o.id = r.owner_id
		JOIN users au ON au.id = x.author_id
		WHERE x.state IN ('open', 'source_gone') AND `+involvedCond+`
		ORDER BY x.updated_at DESC LIMIT 50`, userID)
}

// DashboardIssues is the issue counterpart of DashboardMRs.
func (s *Store) DashboardIssues(userID int64) ([]DashboardItem, error) {
	return s.dashboardQuery(`
		SELECT COALESCE(u.username, o.name) || '/' || r.name,
		       x.number, x.title, au.username, x.state, x.updated_at
		FROM issues x
		JOIN repos r ON r.id = x.repo_id
		LEFT JOIN users u ON r.owner_kind = 'user' AND u.id = r.owner_id
		LEFT JOIN orgs o  ON r.owner_kind = 'org'  AND o.id = r.owner_id
		JOIN users au ON au.id = x.author_id
		WHERE x.state = 'open' AND `+involvedCond+`
		ORDER BY x.updated_at DESC LIMIT 50`, userID)
}

func (s *Store) PinRepo(userID, repoID int64) error {
	_, err := s.DB.Exec(
		"INSERT INTO repo_pins (user_id, repo_id) VALUES (?, ?) ON CONFLICT DO NOTHING",
		userID, repoID)
	return err
}

func (s *Store) IsPinned(userID, repoID int64) bool {
	var n int
	s.DB.QueryRow("SELECT COUNT(*) FROM repo_pins WHERE user_id = ? AND repo_id = ?",
		userID, repoID).Scan(&n)
	return n > 0
}

func (s *Store) UnpinRepo(userID, repoID int64) error {
	res, err := s.DB.Exec(
		"DELETE FROM repo_pins WHERE user_id = ? AND repo_id = ?", userID, repoID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// PinnedRepos returns the user's pinned repositories in pin order. The
// caller applies visibility checks before rendering.
func (s *Store) PinnedRepos(userID int64) ([]Repo, error) {
	rows, err := s.DB.Query(repoSelect+`
		JOIN repo_pins p ON p.repo_id = r.id AND p.user_id = ?
		ORDER BY p.pinned_at`, userID)
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

// ReviewQueue returns open merge requests the user is involved in, has not
// authored, and has not reviewed at the current head — what the rail shows
// as waiting on them. Ordered most recently touched first.
func (s *Store) ReviewQueue(userID int64) ([]DashboardItem, error) {
	return s.dashboardQuery(`
		SELECT COALESCE(u.username, o.name) || '/' || r.name,
		       x.number, x.title, au.username, x.state, x.updated_at
		FROM merge_requests x
		JOIN repos r ON r.id = x.repo_id
		LEFT JOIN users u ON r.owner_kind = 'user' AND u.id = r.owner_id
		LEFT JOIN orgs o  ON r.owner_kind = 'org'  AND o.id = r.owner_id
		JOIN users au ON au.id = x.author_id
		WHERE x.state IN ('open', 'source_gone')
		  AND x.author_id <> ?1
		  AND NOT EXISTS (SELECT 1 FROM mr_reviews rv
		                  WHERE rv.mr_id = x.id AND rv.reviewer_id = ?1
		                    AND rv.head_sha = x.head_sha)
		  AND `+involvedCond+`
		ORDER BY x.updated_at DESC LIMIT 8`, userID)
}

// OpenCounts returns the repo's open issue and open merge request counts,
// for the repo tab badges.
func (s *Store) OpenCounts(repoID int64) (issues, mrs int) {
	s.DB.QueryRow("SELECT COUNT(*) FROM issues WHERE repo_id = ? AND state = 'open'",
		repoID).Scan(&issues)
	s.DB.QueryRow("SELECT COUNT(*) FROM merge_requests WHERE repo_id = ? AND state IN ('open', 'source_gone')",
		repoID).Scan(&mrs)
	return
}
