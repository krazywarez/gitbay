package store

// Search across repositories, for the `search` command and the web's
// /search page. Visibility is the same rule everywhere: public plus what
// the user reaches, so an anonymous caller (user id 0) sees only public
// rows rather than a different query.

// visibleCond admits public repositories and anything the user reaches.
const visibleCond = `(r.visibility = 'public' OR ` + reachableCond + `)`

// VisibleRepos returns every repository the user may read, public ones
// included, ordered by owner then name.
func (s *Store) VisibleRepos(userID int64) ([]Repo, error) {
	rows, err := s.DB.Query(repoSelect+" WHERE "+visibleCond+" ORDER BY 4, r.name", userID)
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

// SearchIssues returns issues matching q in title or body, newest
// activity first.
func (s *Store) SearchIssues(userID int64, q string, limit int) ([]DashboardItem, error) {
	return s.searchQuery("issues", "issue_fts", userID, q, limit)
}

// SearchMRs is SearchIssues for merge requests.
func (s *Store) SearchMRs(userID int64, q string, limit int) ([]DashboardItem, error) {
	return s.searchQuery("merge_requests", "mr_fts", userID, q, limit)
}

func (s *Store) searchQuery(table, index string, userID int64, q string, limit int) ([]DashboardItem, error) {
	rows, err := s.DB.Query(`
		SELECT COALESCE(u.username, o.name) || '/' || r.name,
		       x.number, x.title, au.username, x.state, x.updated_at
		FROM `+table+` x
		JOIN repos r ON r.id = x.repo_id
		LEFT JOIN users u ON r.owner_kind = 'user' AND u.id = r.owner_id
		LEFT JOIN orgs o  ON r.owner_kind = 'org'  AND o.id = r.owner_id
		JOIN users au ON au.id = x.author_id
		WHERE x.id IN (SELECT rowid FROM `+index+` WHERE `+index+` MATCH ?2)
		  AND `+visibleCond+`
		ORDER BY x.updated_at DESC LIMIT ?3`, userID, FTSQuery(q), limit)
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
