package store

import (
	"database/sql"
	"errors"
)

// BotUsername authors dependency-update issues. The account exists from
// migration 0028 with no key and no email: it authors, it never
// authenticates.
const BotUsername = "gitbay-bot"

// DepCheck is a repo's opt-in dependency sweep state. A row exists only
// while checking is enabled.
type DepCheck struct {
	RepoID      int64
	LastCheck   string
	LastError   string
	IssueNumber int64 // 0 until an issue has been opened
}

// DepReport is one dependency found to be behind, as last reported.
type DepReport struct {
	Ecosystem string
	Name      string
	Current   string
	Latest    string
}

func (s *Store) EnableDepCheck(repoID int64) error {
	_, err := s.DB.Exec("INSERT OR IGNORE INTO dep_checks (repo_id) VALUES (?)", repoID)
	return err
}

// DisableDepCheck stops checking and forgets what was reported, so
// re-enabling reports the current state afresh.
func (s *Store) DisableDepCheck(repoID int64) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec("DELETE FROM dep_reports WHERE repo_id = ?", repoID); err != nil {
		return err
	}
	if _, err := tx.Exec("DELETE FROM dep_checks WHERE repo_id = ?", repoID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) DepCheckFor(repoID int64) (DepCheck, error) {
	var d DepCheck
	err := s.DB.QueryRow(
		"SELECT repo_id, last_check, last_error, issue_number FROM dep_checks WHERE repo_id = ?", repoID).
		Scan(&d.RepoID, &d.LastCheck, &d.LastError, &d.IssueNumber)
	if errors.Is(err, sql.ErrNoRows) {
		return d, ErrNotFound
	}
	return d, err
}

// DueDepChecks returns repos whose last check is older than
// intervalSeconds. Archived repos are skipped: nobody is going to act on
// the issue.
func (s *Store) DueDepChecks(intervalSeconds int) ([]Repo, error) {
	rows, err := s.DB.Query(repoSelect+`
		JOIN dep_checks d ON d.repo_id = r.id
		WHERE d.last_check = ''
		   OR strftime('%s','now') - strftime('%s', d.last_check) > ?
		ORDER BY r.id`, intervalSeconds)
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
		if r.Settings.Archived {
			continue
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// SetDepCheckResult stamps a sweep. An empty checkErr records success.
func (s *Store) SetDepCheckResult(repoID int64, checkErr string) error {
	_, err := s.DB.Exec(`
		UPDATE dep_checks SET last_error = ?,
		       last_check = strftime('%Y-%m-%dT%H:%M:%fZ','now')
		WHERE repo_id = ?`, checkErr, repoID)
	return err
}

func (s *Store) SetDepIssue(repoID, number int64) error {
	_, err := s.DB.Exec("UPDATE dep_checks SET issue_number = ? WHERE repo_id = ?", number, repoID)
	return err
}

func (s *Store) ReportedDeps(repoID int64) ([]DepReport, error) {
	rows, err := s.DB.Query(`
		SELECT ecosystem, name, current, latest FROM dep_reports
		WHERE repo_id = ? ORDER BY ecosystem, name`, repoID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DepReport
	for rows.Next() {
		var d DepReport
		if err := rows.Scan(&d.Ecosystem, &d.Name, &d.Current, &d.Latest); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// ReplaceDepReports swaps in the current outdated set wholesale: a
// dependency that was updated, removed, or renamed leaves no trace.
func (s *Store) ReplaceDepReports(repoID int64, reports []DepReport) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec("DELETE FROM dep_reports WHERE repo_id = ?", repoID); err != nil {
		return err
	}
	for _, d := range reports {
		if _, err := tx.Exec(`
			INSERT INTO dep_reports (repo_id, ecosystem, name, current, latest)
			VALUES (?, ?, ?, ?, ?)`, repoID, d.Ecosystem, d.Name, d.Current, d.Latest); err != nil {
			return err
		}
	}
	return tx.Commit()
}
