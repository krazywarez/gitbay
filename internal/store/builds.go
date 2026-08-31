package store

import (
	"database/sql"
	"errors"
	"strings"
	"time"
)

// Build is one CI job execution for one commit.
type Build struct {
	ID         int64
	RepoID     int64
	Number     int64
	Job        string
	SHA        string
	Ref        string
	Steps      string // JSON array of shell commands
	Status     string // pending|running|success|failure
	CreatedAt  string
	StartedAt  string
	FinishedAt string
}

// MaxBuildLog caps a build's stored log; appends past it are dropped.
const MaxBuildLog = 2 << 20

// CreateBuild allocates the per-repo build number in the same transaction
// as the insert, like issue and MR numbers.
func (s *Store) CreateBuild(repoID int64, job, sha, ref, stepsJSON string) (int64, error) {
	tx, err := s.DB.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec("UPDATE repos SET build_counter = build_counter + 1 WHERE id = ?", repoID); err != nil {
		return 0, err
	}
	var n int64
	if err := tx.QueryRow("SELECT build_counter FROM repos WHERE id = ?", repoID).Scan(&n); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(
		"INSERT INTO builds (repo_id, number, job, sha, ref, steps) VALUES (?, ?, ?, ?, ?, ?)",
		repoID, n, job, sha, ref, stepsJSON); err != nil {
		return 0, err
	}
	return n, tx.Commit()
}

const buildSelect = `
	SELECT id, repo_id, number, job, sha, ref, steps, status, created_at, started_at, finished_at
	FROM builds`

func scanBuild(row interface{ Scan(...any) error }) (Build, error) {
	var b Build
	err := row.Scan(&b.ID, &b.RepoID, &b.Number, &b.Job, &b.SHA, &b.Ref, &b.Steps,
		&b.Status, &b.CreatedAt, &b.StartedAt, &b.FinishedAt)
	return b, err
}

// ClaimBuild atomically hands the oldest pending build to a runner.
// ClaimBuild takes the oldest pending build and marks it running. A
// non-empty repoIDs restricts the claim to those repositories, which is how
// a runner on a machine that should not execute every repository's steps
// limits what it picks up.
func (s *Store) ClaimBuild(repoIDs []int64) (Build, bool, error) {
	tx, err := s.DB.Begin()
	if err != nil {
		return Build{}, false, err
	}
	defer tx.Rollback()
	query := "SELECT id FROM builds WHERE status = 'pending' ORDER BY id LIMIT 1"
	args := []any{}
	if len(repoIDs) > 0 {
		marks := strings.TrimSuffix(strings.Repeat("?,", len(repoIDs)), ",")
		query = "SELECT id FROM builds WHERE status = 'pending' AND repo_id IN (" +
			marks + ") ORDER BY id LIMIT 1"
		for _, id := range repoIDs {
			args = append(args, id)
		}
	}
	var id int64
	err = tx.QueryRow(query, args...).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return Build{}, false, nil
	}
	if err != nil {
		return Build{}, false, err
	}
	if _, err := tx.Exec(
		"UPDATE builds SET status = 'running', started_at = strftime('%Y-%m-%dT%H:%M:%SZ','now') WHERE id = ?", id); err != nil {
		return Build{}, false, err
	}
	b, err := scanBuild(tx.QueryRow(buildSelect+" WHERE id = ?", id))
	if err != nil {
		return Build{}, false, err
	}
	return b, true, tx.Commit()
}

// StaleBuildDeadline is how long a claimed build may stay running before the
// server gives up on it. Comfortably longer than the runner's own -timeout
// (45m by default), so this only fires when the runner never reported at all —
// it was killed, restarted, or lost the network mid-build.
const StaleBuildDeadline = 90 * time.Minute

// ReapStaleBuilds fails every build that has been running past the deadline and
// returns them, so the caller can resolve their commit statuses. A runner that
// dies between claiming a build and reporting it otherwise leaves the row
// claimed forever, and the commit pending forever with it.
func (s *Store) ReapStaleBuilds() ([]Build, error) {
	cutoff := time.Now().UTC().Add(-StaleBuildDeadline).Format("2006-01-02T15:04:05Z")
	rows, err := s.DB.Query(buildSelect+
		" WHERE status = 'running' AND started_at != '' AND started_at < ?", cutoff)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var stale []Build
	for rows.Next() {
		b, err := scanBuild(rows)
		if err != nil {
			return nil, err
		}
		stale = append(stale, b)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, b := range stale {
		if err := s.AppendBuildLog(b.ID, []byte(
			"\nbuild abandoned: the runner never reported an outcome\n")); err != nil {
			return nil, err
		}
		if err := s.FinishBuild(b.ID, "failure"); err != nil {
			return nil, err
		}
	}
	return stale, nil
}

// AppendBuildLog adds a chunk to the build's log, dropping bytes past the cap.
func (s *Store) AppendBuildLog(id int64, chunk []byte) error {
	_, err := s.DB.Exec(`
		UPDATE builds SET log = log || ?
		WHERE id = ? AND length(log) < ?`, chunk, id, MaxBuildLog)
	return err
}

// FinishBuild records the outcome of a running build.
func (s *Store) FinishBuild(id int64, status string) error {
	res, err := s.DB.Exec(`
		UPDATE builds SET status = ?, finished_at = strftime('%Y-%m-%dT%H:%M:%SZ','now')
		WHERE id = ? AND status = 'running'`, status, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) BuildByID(id int64) (Build, error) {
	b, err := scanBuild(s.DB.QueryRow(buildSelect+" WHERE id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return b, ErrNotFound
	}
	return b, err
}

func (s *Store) BuildByNumber(repoID, number int64) (Build, error) {
	b, err := scanBuild(s.DB.QueryRow(buildSelect+" WHERE repo_id = ? AND number = ?", repoID, number))
	if errors.Is(err, sql.ErrNoRows) {
		return b, ErrNotFound
	}
	return b, err
}

func (s *Store) ListBuilds(repoID int64, limit int) ([]Build, error) {
	rows, err := s.DB.Query(buildSelect+" WHERE repo_id = ? ORDER BY number DESC LIMIT ?", repoID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Build
	for rows.Next() {
		b, err := scanBuild(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// BuildLog returns the stored log bytes.
func (s *Store) BuildLog(id int64) ([]byte, error) {
	var log []byte
	err := s.DB.QueryRow("SELECT log FROM builds WHERE id = ?", id).Scan(&log)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return log, err
}

// LatestBuild returns the newest build for a repo, optionally narrowed to
// one job. It is what a status badge reports.
func (s *Store) LatestBuild(repoID int64, job string) (Build, error) {
	q := buildSelect + " WHERE repo_id = ?"
	args := []any{repoID}
	if job != "" {
		q += " AND job = ?"
		args = append(args, job)
	}
	q += " ORDER BY number DESC LIMIT 1"
	b, err := scanBuild(s.DB.QueryRow(q, args...))
	if errors.Is(err, sql.ErrNoRows) {
		return b, ErrNotFound
	}
	return b, err
}

// BuildsForCommit returns the newest build per job for one commit. A merge
// request's checks are ci/<job> statuses; this is where their timing comes
// from, in one query rather than one per check.
func (s *Store) BuildsForCommit(repoID int64, sha string) (map[string]Build, error) {
	rows, err := s.DB.Query(buildSelect+" WHERE repo_id = ? AND sha = ? ORDER BY number ASC", repoID, sha)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]Build{}
	for rows.Next() {
		b, err := scanBuild(rows)
		if err != nil {
			return nil, err
		}
		out[b.Job] = b // ascending: the last row for a job wins
	}
	return out, rows.Err()
}

// Elapsed reports how long a build ran. Zero until it has both a start and
// a finish, which is every state but success and failure.
func (b Build) Elapsed() time.Duration {
	const layout = "2006-01-02T15:04:05Z"
	start, err := time.Parse(layout, b.StartedAt)
	if err != nil {
		return 0
	}
	end, err := time.Parse(layout, b.FinishedAt)
	if err != nil {
		return 0
	}
	if d := end.Sub(start); d > 0 {
		return d.Round(time.Second)
	}
	return 0
}
