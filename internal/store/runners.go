package store

import "sort"

// Runner is one runner key as the instance admin sees it.
type Runner struct {
	Username    string `json:"username"`
	Fingerprint string `json:"fingerprint"`
	KeyID       int64  `json:"-"`
	LastSeen    string `json:"last_seen"`
	// Scope is what the runner asked for: comma-joined owner/name, ""
	// for any. admin runners replaces it with the attachments for a
	// runner key.
	Scope string `json:"scope,omitempty"`
	// The build it holds, if any.
	BuildRepo   string `json:"build_repo,omitempty"`
	BuildNumber int64  `json:"build_number,omitempty"`
	BuildJob    string `json:"build_job,omitempty"`
	StartedAt   string `json:"started_at,omitempty"`
}

// RepoRunner is one key attached to a repository, as repo runner list
// shows it.
type RepoRunner struct {
	Fingerprint string `json:"fingerprint"`
	Algo        string `json:"algo"`
	Username    string `json:"username"`
	AddedAt     string `json:"added_at"`
	LastSeen    string `json:"last_seen,omitempty"`
	BuildRepo   string `json:"build_repo,omitempty"`
	BuildNumber int64  `json:"build_number,omitempty"`
	BuildJob    string `json:"build_job,omitempty"`
	StartedAt   string `json:"started_at,omitempty"`
}

// AttachRunner lets a key claim a repository's builds. Attaching twice is
// one row.
func (s *Store) AttachRunner(keyID, repoID int64) error {
	_, err := s.DB.Exec("INSERT OR IGNORE INTO runner_repos (key_id, repo_id) VALUES (?, ?)", keyID, repoID)
	return err
}

// DetachRunner removes one attachment by fingerprint. The key itself stays.
func (s *Store) DetachRunner(repoID int64, fingerprint string) error {
	res, err := s.DB.Exec(`DELETE FROM runner_repos WHERE repo_id = ?
		AND key_id = (SELECT id FROM ssh_keys WHERE fingerprint = ?)`, repoID, fingerprint)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// RunnerRepoIDs is every repository a key is attached to.
func (s *Store) RunnerRepoIDs(keyID int64) ([]int64, error) {
	rows, err := s.DB.Query("SELECT repo_id FROM runner_repos WHERE key_id = ? ORDER BY repo_id", keyID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// RunnerRepoPaths is RunnerRepoIDs as owner/name, sorted.
func (s *Store) RunnerRepoPaths(keyID int64) ([]string, error) {
	rows, err := s.DB.Query(`SELECT COALESCE(u.username, o.name) || '/' || r.name
		FROM runner_repos rr JOIN repos r ON r.id = rr.repo_id
		LEFT JOIN users u ON r.owner_kind = 'user' AND u.id = r.owner_id
		LEFT JOIN orgs o ON r.owner_kind = 'org' AND o.id = r.owner_id
		WHERE rr.key_id = ?`, keyID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var paths []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		paths = append(paths, p)
	}
	sort.Strings(paths)
	return paths, rows.Err()
}

// RunnerAttached reports whether a key may claim a repository's builds.
func (s *Store) RunnerAttached(keyID, repoID int64) (bool, error) {
	var n int
	err := s.DB.QueryRow("SELECT count(*) FROM runner_repos WHERE key_id = ? AND repo_id = ?", keyID, repoID).Scan(&n)
	return n > 0, err
}

// ListRepoRunners is every key attached to a repository with its last
// poll and the build it holds, oldest attachment first.
func (s *Store) ListRepoRunners(repoID int64) ([]RepoRunner, error) {
	rows, err := s.DB.Query(`SELECT k.fingerprint, k.algo, u.username, rr.added_at,
		COALESCE(rs.last_seen, ''),
		COALESCE(COALESCE(bu.username, bo.name) || '/' || br.name, ''),
		COALESCE(b.number, 0), COALESCE(b.job, ''), COALESCE(b.started_at, '')
		FROM runner_repos rr
		JOIN ssh_keys k ON k.id = rr.key_id
		JOIN users u ON u.id = k.user_id
		LEFT JOIN runner_seen rs ON rs.key_id = rr.key_id
		LEFT JOIN builds b ON b.id = rs.build_id AND b.status = 'running'
		LEFT JOIN repos br ON br.id = b.repo_id
		LEFT JOIN users bu ON br.owner_kind = 'user' AND bu.id = br.owner_id
		LEFT JOIN orgs bo ON br.owner_kind = 'org' AND bo.id = br.owner_id
		WHERE rr.repo_id = ? ORDER BY rr.added_at, k.id`, repoID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RepoRunner
	for rows.Next() {
		var r RepoRunner
		if err := rows.Scan(&r.Fingerprint, &r.Algo, &r.Username, &r.AddedAt, &r.LastSeen,
			&r.BuildRepo, &r.BuildNumber, &r.BuildJob, &r.StartedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// TouchRunner records a poll by one key: the time, the scope the runner
// asked for, and the build it just claimed (0 for none).
func (s *Store) TouchRunner(keyID, userID int64, scope string, buildID int64) error {
	_, err := s.DB.Exec(`INSERT INTO runner_seen (key_id, user_id, last_seen, scope, build_id)
		VALUES (?1, ?2, strftime('%Y-%m-%dT%H:%M:%fZ','now'), ?3, NULLIF(?4, 0))
		ON CONFLICT (key_id) DO UPDATE SET
			last_seen = excluded.last_seen, scope = excluded.scope,
			build_id = COALESCE(excluded.build_id, runner_seen.build_id)`,
		keyID, userID, scope, buildID)
	return err
}

// RunnerDone records that the key reported and holds nothing now.
func (s *Store) RunnerDone(keyID int64) error {
	_, err := s.DB.Exec(`UPDATE runner_seen SET last_seen = strftime('%Y-%m-%dT%H:%M:%fZ','now'),
		build_id = NULL WHERE key_id = ?`, keyID)
	return err
}

// ListRunners lists every key that has ever polled as a runner, most
// recently seen first.
func (s *Store) ListRunners() ([]Runner, error) {
	rows, err := s.DB.Query(`SELECT u.username, k.fingerprint, k.id, r.last_seen, r.scope,
		COALESCE(COALESCE(bu.username, bo.name) || '/' || br.name, ''),
		COALESCE(b.number, 0), COALESCE(b.job, ''), COALESCE(b.started_at, '')
		FROM runner_seen r JOIN users u ON u.id = r.user_id
		JOIN ssh_keys k ON k.id = r.key_id
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
		if err := rows.Scan(&r.Username, &r.Fingerprint, &r.KeyID, &r.LastSeen, &r.Scope,
			&r.BuildRepo, &r.BuildNumber, &r.BuildJob, &r.StartedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
