package store

// SetBuildSecret stores or replaces one secret. The value never leaves the
// server except inside a claimed build's environment.
func (s *Store) SetBuildSecret(repoID int64, name, value string) error {
	_, err := s.DB.Exec(`
		INSERT INTO build_secrets (repo_id, name, value) VALUES (?, ?, ?)
		ON CONFLICT (repo_id, name) DO UPDATE SET value = excluded.value`,
		repoID, name, value)
	return err
}

func (s *Store) RemoveBuildSecret(repoID int64, name string) error {
	res, err := s.DB.Exec("DELETE FROM build_secrets WHERE repo_id = ? AND name = ?", repoID, name)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ListBuildSecretNames returns names only; values are for builds.
func (s *Store) ListBuildSecretNames(repoID int64) ([]string, error) {
	rows, err := s.DB.Query("SELECT name FROM build_secrets WHERE repo_id = ? ORDER BY name", repoID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// BuildSecrets returns the values, for injection into a claimed build.
func (s *Store) BuildSecrets(repoID int64) (map[string]string, error) {
	rows, err := s.DB.Query("SELECT name, value FROM build_secrets WHERE repo_id = ?", repoID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var n, v string
		if err := rows.Scan(&n, &v); err != nil {
			return nil, err
		}
		out[n] = v
	}
	return out, rows.Err()
}

// Schedule is one repo job's cron entry.
type Schedule struct {
	RepoID  int64
	Job     string
	Cron    string
	NextRun string
}

// SyncSchedules replaces a repo's schedule set with the given entries,
// preserving next_run for entries whose cron is unchanged.
func (s *Store) SyncSchedules(repoID int64, entries []Schedule) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	keep := map[string]bool{}
	for _, e := range entries {
		keep[e.Job] = true
		if _, err := tx.Exec(`
			INSERT INTO build_schedules (repo_id, job, cron, next_run) VALUES (?, ?, ?, ?)
			ON CONFLICT (repo_id, job) DO UPDATE SET
				next_run = CASE WHEN cron = excluded.cron THEN next_run ELSE excluded.next_run END,
				cron = excluded.cron`,
			repoID, e.Job, e.Cron, e.NextRun); err != nil {
			return err
		}
	}
	rows, err := tx.Query("SELECT job FROM build_schedules WHERE repo_id = ?", repoID)
	if err != nil {
		return err
	}
	var stale []string
	for rows.Next() {
		var j string
		if err := rows.Scan(&j); err != nil {
			rows.Close()
			return err
		}
		if !keep[j] {
			stale = append(stale, j)
		}
	}
	rows.Close()
	for _, j := range stale {
		if _, err := tx.Exec("DELETE FROM build_schedules WHERE repo_id = ? AND job = ?", repoID, j); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// DueSchedules returns entries whose next_run is at or before now.
func (s *Store) DueSchedules(nowISO string) ([]Schedule, error) {
	rows, err := s.DB.Query(
		"SELECT repo_id, job, cron, next_run FROM build_schedules WHERE next_run <= ? ORDER BY next_run", nowISO)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Schedule
	for rows.Next() {
		var e Schedule
		if err := rows.Scan(&e.RepoID, &e.Job, &e.Cron, &e.NextRun); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// SetScheduleNext advances one entry's next firing time.
func (s *Store) SetScheduleNext(repoID int64, job, nextRun string) error {
	_, err := s.DB.Exec(
		"UPDATE build_schedules SET next_run = ? WHERE repo_id = ? AND job = ?", nextRun, repoID, job)
	return err
}

func (s *Store) RemoveSchedule(repoID int64, job string) error {
	_, err := s.DB.Exec("DELETE FROM build_schedules WHERE repo_id = ? AND job = ?", repoID, job)
	return err
}
