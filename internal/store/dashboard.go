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

// reachableCond filters to repositories the user owns, is granted on, or
// reaches through org or team membership.
const reachableCond = `(
	(r.owner_kind = 'user' AND r.owner_id = ?1)
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

// involvedCond widens reachableCond to rows the user authored anywhere.
const involvedCond = `(x.author_id = ?1 OR ` + reachableCond + `)`

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

// AssignedIssues returns open issues assigned to the user, wherever they
// live. Assignment is a direct request for someone's attention, so it is
// not narrowed by the involvement rule the other lists use.
func (s *Store) AssignedIssues(userID int64) ([]DashboardItem, error) {
	return s.dashboardQuery(`
		SELECT COALESCE(u.username, o.name) || '/' || r.name,
		       x.number, x.title, au.username, x.state, x.updated_at
		FROM issues x
		JOIN repos r ON r.id = x.repo_id
		LEFT JOIN users u ON r.owner_kind = 'user' AND u.id = r.owner_id
		LEFT JOIN orgs o  ON r.owner_kind = 'org'  AND o.id = r.owner_id
		JOIN users au ON au.id = x.author_id
		WHERE x.state = 'open'
		  AND EXISTS (SELECT 1 FROM issue_assignees ia
		              WHERE ia.issue_id = x.id AND ia.user_id = ?1)
		ORDER BY x.updated_at DESC LIMIT 20`, userID)
}

// DashboardBuild is one build row on the dashboard, with its repo resolved.
type DashboardBuild struct {
	RepoPath   string
	Number     int64
	Job        string
	Status     string
	SHA        string
	Ref        string
	CreatedAt  string
	FinishedAt string
}

// RecentBuilds returns the newest builds on repositories the user can
// reach, most recent first.
func (s *Store) RecentBuilds(userID int64, limit int) ([]DashboardBuild, error) {
	rows, err := s.DB.Query(`
		SELECT COALESCE(u.username, o.name) || '/' || r.name,
		       b.number, b.job, b.status, b.sha, b.ref, b.created_at, b.finished_at
		FROM builds b
		JOIN repos r ON r.id = b.repo_id
		LEFT JOIN users u ON r.owner_kind = 'user' AND u.id = r.owner_id
		LEFT JOIN orgs o  ON r.owner_kind = 'org'  AND o.id = r.owner_id
		WHERE `+reachableCond+`
		ORDER BY b.id DESC LIMIT ?2`, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DashboardBuild
	for rows.Next() {
		var b DashboardBuild
		if err := rows.Scan(&b.RepoPath, &b.Number, &b.Job, &b.Status, &b.SHA, &b.Ref, &b.CreatedAt, &b.FinishedAt); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// FeedEvent is one line of the dashboard's activity feed.
type FeedEvent struct {
	RepoPath  string
	Actor     string
	Kind      string
	Data      string
	CreatedAt string
}

// RecentEvents returns activity on repositories the user can reach. Push
// events are excluded: they repeat what the commit lists already show.
func (s *Store) RecentEvents(userID int64, limit int) ([]FeedEvent, error) {
	rows, err := s.DB.Query(`
		SELECT COALESCE(u.username, o.name) || '/' || r.name,
		       COALESCE(ac.username, ''), e.kind, e.data_json, e.created_at
		FROM events e
		JOIN repos r ON r.id = e.repo_id
		LEFT JOIN users u ON r.owner_kind = 'user' AND u.id = r.owner_id
		LEFT JOIN orgs o  ON r.owner_kind = 'org'  AND o.id = r.owner_id
		LEFT JOIN users ac ON ac.id = e.actor_id
		WHERE e.kind <> 'push' AND `+reachableCond+`
		ORDER BY e.id DESC LIMIT ?2`, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []FeedEvent
	for rows.Next() {
		var e FeedEvent
		if err := rows.Scan(&e.RepoPath, &e.Actor, &e.Kind, &e.Data, &e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
