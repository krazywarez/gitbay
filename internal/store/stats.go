package store

// ListAllRepos returns every repository, for host-local admin tooling.
func (s *Store) ListAllRepos() ([]Repo, error) {
	rows, err := s.DB.Query(repoSelect + " ORDER BY 4, r.name")
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

type Counts struct {
	Users      int64 `json:"users"`
	Orgs       int64 `json:"orgs"`
	Repos      int64 `json:"repos"`
	Issues     int64 `json:"issues"`
	OpenIssues int64 `json:"open_issues"`
	MRs        int64 `json:"mrs"`
	OpenMRs    int64 `json:"open_mrs"`
}

func (s *Store) InstanceCounts() (Counts, error) {
	var c Counts
	for _, q := range []struct {
		dst   *int64
		query string
	}{
		{&c.Users, "SELECT COUNT(*) FROM users"},
		{&c.Orgs, "SELECT COUNT(*) FROM orgs"},
		{&c.Repos, "SELECT COUNT(*) FROM repos"},
		{&c.Issues, "SELECT COUNT(*) FROM issues"},
		{&c.OpenIssues, "SELECT COUNT(*) FROM issues WHERE state = 'open'"},
		{&c.MRs, "SELECT COUNT(*) FROM merge_requests"},
		{&c.OpenMRs, "SELECT COUNT(*) FROM merge_requests WHERE state IN ('open','source_gone')"},
	} {
		if err := s.DB.QueryRow(q.query).Scan(q.dst); err != nil {
			return c, err
		}
	}
	return c, nil
}
