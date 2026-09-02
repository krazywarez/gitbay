package store

// Runner is one runner account as the instance admin sees it.
type Runner struct {
	Username string `json:"username"`
	LastSeen string `json:"last_seen"`
	Scope    string `json:"scope,omitempty"` // comma-joined owner/name, "" for any
	// The build it holds, if any.
	BuildRepo   string `json:"build_repo,omitempty"`
	BuildNumber int64  `json:"build_number,omitempty"`
	BuildJob    string `json:"build_job,omitempty"`
	StartedAt   string `json:"started_at,omitempty"`
}

// TouchRunner records a poll: the time, the scope the runner asked for,
// and the build it just claimed (0 for none).
func (s *Store) TouchRunner(userID int64, scope string, buildID int64) error {
	_, err := s.DB.Exec(`INSERT INTO runner_seen (user_id, last_seen, scope, build_id)
		VALUES (?1, strftime('%Y-%m-%dT%H:%M:%fZ','now'), ?2, NULLIF(?3, 0))
		ON CONFLICT (user_id) DO UPDATE SET
			last_seen = excluded.last_seen, scope = excluded.scope,
			build_id = COALESCE(excluded.build_id, runner_seen.build_id)`,
		userID, scope, buildID)
	return err
}

// RunnerDone records that the runner reported and holds nothing now.
func (s *Store) RunnerDone(userID int64) error {
	_, err := s.DB.Exec(`UPDATE runner_seen SET last_seen = strftime('%Y-%m-%dT%H:%M:%fZ','now'),
		build_id = NULL WHERE user_id = ?`, userID)
	return err
}

// ListRunners lists every account that has ever polled as a runner,
// most recently seen first.
func (s *Store) ListRunners() ([]Runner, error) {
	rows, err := s.DB.Query(`SELECT u.username, r.last_seen, r.scope,
		COALESCE(COALESCE(bu.username, bo.name) || '/' || br.name, ''),
		COALESCE(b.number, 0), COALESCE(b.job, ''), COALESCE(b.started_at, '')
		FROM runner_seen r JOIN users u ON u.id = r.user_id
		LEFT JOIN builds b ON b.id = r.build_id AND b.status = 'running'
		LEFT JOIN repos br ON br.id = b.repo_id
		LEFT JOIN users bu ON br.owner_kind = 'user' AND bu.id = br.owner_id
		LEFT JOIN orgs bo ON br.owner_kind = 'org' AND bo.id = br.owner_id
		ORDER BY r.last_seen DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Runner
	for rows.Next() {
		var r Runner
		if err := rows.Scan(&r.Username, &r.LastSeen, &r.Scope, &r.BuildRepo, &r.BuildNumber, &r.BuildJob, &r.StartedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
