package store

import (
	"database/sql"
	"errors"
	"os"
	"strconv"
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
	Image      string // container image for the steps; "" means the runner default
	Tree       string // the commit's tree; "" when not deduplicated by tree
	Status     string // pending|running|success|failure
	CreatedAt  string
	StartedAt  string
	FinishedAt string
	// LogClosedAt is when the runner's log stream ended; "" while it is
	// open or was never opened. Set on a running build only.
	LogClosedAt string
	// Trusted is false for a merge request head fetched from another
	// repository: its steps run without the target's secrets.
	Trusted bool
}

// MaxBuildLog caps a build's stored log; appends past it are dropped.
const MaxBuildLog = 2 << 20

// truncNotice is appended once when a log first hits the cap. A log that
// simply stops is indistinguishable from a build that died mid-step, which
// is the reading that sent people hunting for a nonexistent test failure.
var truncNotice = []byte("\n[log truncated: reached the " +
	strconv.Itoa(MaxBuildLog>>20) + " MiB cap; earlier output is above]\n")

// CreateBuild allocates the per-repo build number in the same transaction
// as the insert, like issue and MR numbers.
func (s *Store) CreateBuild(repoID int64, job, sha, ref, stepsJSON, image, tree string, trusted bool) (int64, error) {
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
		"INSERT INTO builds (repo_id, number, job, sha, ref, steps, image, tree, trusted) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)",
		repoID, n, job, sha, ref, stepsJSON, image, tree, trusted); err != nil {
		return 0, err
	}
	return n, tx.Commit()
}

const buildSelect = `
	SELECT id, repo_id, number, job, sha, ref, steps, image, tree, status, created_at, started_at, finished_at, log_closed_at, trusted
	FROM builds`

func scanBuild(row interface{ Scan(...any) error }) (Build, error) {
	var b Build
	var trusted int
	err := row.Scan(&b.ID, &b.RepoID, &b.Number, &b.Job, &b.SHA, &b.Ref, &b.Steps, &b.Image, &b.Tree,
		&b.Status, &b.CreatedAt, &b.StartedAt, &b.FinishedAt, &b.LogClosedAt, &trusted)
	b.Trusted = trusted != 0
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

// staleBuildDeadline is StaleBuildDeadline unless GITBAY_STALE_BUILD_DEADLINE
// shortens it, which tests do.
func staleBuildDeadline() time.Duration {
	if v := os.Getenv("GITBAY_STALE_BUILD_DEADLINE"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return StaleBuildDeadline
}

// StaleLogGrace is how long a running build may go on after its log
// stream ended before it is treated as abandoned. The runner reports the
// outcome right after closing the stream, retrying for about thirty
// seconds if the server is unreachable; two minutes outlasts that.
const StaleLogGrace = 2 * time.Minute

// MarkBuildLogClosed records that the runner's log stream for a build
// ended, on a build still running. A build that finishes normally is
// reported moments later and the mark is moot; one that is not has lost
// its runner, and ReapStaleBuilds fails it after StaleLogGrace rather
// than at the deadline (#179).
func (s *Store) MarkBuildLogClosed(id int64) error {
	_, err := s.DB.Exec(`
		UPDATE builds SET log_closed_at = strftime('%Y-%m-%dT%H:%M:%SZ','now')
		WHERE id = ? AND status = 'running' AND log_closed_at = ''`, id)
	return err
}

// ReapStaleBuilds fails every running build whose runner is gone and
// returns them, so the caller can resolve their commit statuses: one whose
// log stream ended more than StaleLogGrace ago with no outcome reported,
// or one running past the deadline with no stream ever seen. A runner that
// dies between claiming a build and reporting it otherwise leaves the row
// claimed forever, and the commit pending forever with it.
func (s *Store) ReapStaleBuilds() ([]Build, error) {
	const layout = "2006-01-02T15:04:05Z"
	now := time.Now().UTC()
	cutoff := now.Add(-staleBuildDeadline()).Format(layout)
	logCutoff := now.Add(-StaleLogGrace).Format(layout)
	rows, err := s.DB.Query(buildSelect+
		" WHERE status = 'running' AND ((started_at != '' AND started_at < ?)"+
		" OR (log_closed_at != '' AND log_closed_at < ?))", cutoff, logCutoff)
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
		if _, err := s.DB.Exec(`UPDATE builds SET reaped_at = finished_at WHERE id = ?`, b.ID); err != nil {
			return nil, err
		}
	}
	return stale, nil
}

// QueueStats is the state of the build queue: what waits now, and over
// the last day how long a build waited to be claimed and how many were
// ended by the reaper rather than by a runner's report (#184).
type QueueStats struct {
	Pending       int64 `json:"pending"`
	Claimed24h    int64 `json:"claimed_24h"`
	ClaimWaitAvgS int64 `json:"claim_wait_avg_s"`
	ClaimWaitMaxS int64 `json:"claim_wait_max_s"`
	Reaped24h     int64 `json:"reaped_24h"`
}

func (s *Store) QueueStats() (QueueStats, error) {
	var q QueueStats
	since := time.Now().UTC().Add(-24 * time.Hour).Format("2006-01-02T15:04:05Z")
	err := s.DB.QueryRow(`SELECT
		(SELECT COUNT(*) FROM builds WHERE status = 'pending'),
		COUNT(*),
		COALESCE(AVG(strftime('%s', started_at) - strftime('%s', created_at)), 0),
		COALESCE(MAX(strftime('%s', started_at) - strftime('%s', created_at)), 0),
		(SELECT COUNT(*) FROM builds WHERE reaped_at >= ?)
		FROM builds WHERE started_at >= ?`, since, since).
		Scan(&q.Pending, &q.Claimed24h, &q.ClaimWaitAvgS, &q.ClaimWaitMaxS, &q.Reaped24h)
	return q, err
}

// AppendBuildLog adds a chunk to the build's log, dropping bytes past the cap.
func (s *Store) AppendBuildLog(id int64, chunk []byte) error {
	res, err := s.DB.Exec(`
		UPDATE builds SET log = log || ?
		WHERE id = ? AND length(log) < ?`, chunk, id, MaxBuildLog)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n > 0 {
		return nil
	}
	// Over the cap. The bounds match exactly once: appending the notice puts
	// the log past the upper bound, so later chunks fall through silently.
	_, err = s.DB.Exec(`
		UPDATE builds SET log = log || ?
		WHERE id = ? AND length(log) >= ? AND length(log) < ?`,
		truncNotice, id, MaxBuildLog, MaxBuildLog+len(truncNotice))
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

// CancelBuild withdraws a queued or running build. A running one is
// ended by the runner, which learns of the cancellation when its log
// session is closed, and whose later report lands on a row that already
// says cancelled.
func (s *Store) CancelBuild(id int64) error {
	res, err := s.DB.Exec(`UPDATE builds SET status = 'cancelled',
		finished_at = strftime('%Y-%m-%dT%H:%M:%SZ','now') WHERE id = ? AND status IN ('pending', 'running')`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SuccessBuildFor finds a passed build of the commit for the job, on any
// ref: what a cancelled duplicate can point back at.
// SuccessBuildForTree is SuccessBuildFor keyed by tree rather than
// commit: a rebase that changes nothing in the tree has already been
// built (#177). An empty tree never matches.
func (s *Store) SuccessBuildForTree(repoID int64, tree, job string) (Build, bool, error) {
	if tree == "" {
		return Build{}, false, nil
	}
	b, err := scanBuild(s.DB.QueryRow(buildSelect+
		" WHERE repo_id = ? AND tree = ? AND job = ? AND status = 'success' ORDER BY number DESC LIMIT 1", repoID, tree, job))
	if errors.Is(err, sql.ErrNoRows) {
		return Build{}, false, nil
	}
	return b, err == nil, err
}

func (s *Store) SuccessBuildFor(repoID int64, sha, job string) (Build, bool, error) {
	b, err := scanBuild(s.DB.QueryRow(buildSelect+
		" WHERE repo_id = ? AND sha = ? AND job = ? AND status = 'success' ORDER BY number DESC LIMIT 1", repoID, sha, job))
	if errors.Is(err, sql.ErrNoRows) {
		return Build{}, false, nil
	}
	return b, err == nil, err
}
