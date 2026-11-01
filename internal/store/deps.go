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
	// AdvisoryIssueNumber is the advisories issue, kept apart from the
	// updates one.
	AdvisoryIssueNumber int64
}

// DepReport is one dependency found to be behind, as last reported.
type DepReport struct {
	Ecosystem string
	Name      string
	Current   string
	Latest    string
}

// DepAdvisory is one published advisory matching a resolved dependency,
// as last reported.
type DepAdvisory struct {
	Ecosystem string
	Name      string
	Version   string
	ID        string
	Summary   string
	Severity  string
	Fixed     string // the first release past Version that fixes it, if any
	// NotImported marks a Go advisory whose packages no Go file in the
	// repository imports; it is kept but not reported.
	NotImported bool
	// Imports are the packages the advisory names (Go), for deciding
	// NotImported. Not stored.
	Imports []string
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
	if _, err := tx.Exec("DELETE FROM dep_advisories WHERE repo_id = ?", repoID); err != nil {
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
		"SELECT repo_id, last_check, last_error, issue_number, advisory_issue_number FROM dep_checks WHERE repo_id = ?", repoID).
		Scan(&d.RepoID, &d.LastCheck, &d.LastError, &d.IssueNumber, &d.AdvisoryIssueNumber)
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

func (s *Store) SetDepAdvisoryIssue(repoID, number int64) error {
	_, err := s.DB.Exec("UPDATE dep_checks SET advisory_issue_number = ? WHERE repo_id = ?", number, repoID)
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

func (s *Store) ReportedAdvisories(repoID int64) ([]DepAdvisory, error) {
	rows, err := s.DB.Query(`
		SELECT ecosystem, name, version, advisory_id, summary, severity, fixed, not_imported FROM dep_advisories
		WHERE repo_id = ? ORDER BY ecosystem, name, version, advisory_id`, repoID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DepAdvisory
	for rows.Next() {
		var a DepAdvisory
		if err := rows.Scan(&a.Ecosystem, &a.Name, &a.Version, &a.ID, &a.Summary, &a.Severity, &a.Fixed, &a.NotImported); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// ReplaceDepAdvisories swaps in the current matching set wholesale, as
// ReplaceDepReports does for updates.
func (s *Store) ReplaceDepAdvisories(repoID int64, advisories []DepAdvisory) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec("DELETE FROM dep_advisories WHERE repo_id = ?", repoID); err != nil {
		return err
	}
	for _, a := range advisories {
		if _, err := tx.Exec(`
			INSERT INTO dep_advisories (repo_id, ecosystem, name, version, advisory_id, summary, severity, fixed, not_imported)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			repoID, a.Ecosystem, a.Name, a.Version, a.ID, a.Summary, a.Severity, a.Fixed, a.NotImported); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// AdvisoryDismissal is an advisory a repository dismissed.
type AdvisoryDismissal struct {
	ID        string
	Reason    string
	By        string // username, '' once the account is gone
	CreatedAt string
}

// DismissAdvisories dismisses advisories by id, replacing the reason of
// any already dismissed.
func (s *Store) DismissAdvisories(repoID, byID int64, ids []string, reason string) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, id := range ids {
		if _, err := tx.Exec(`
			INSERT INTO dep_advisory_dismissals (repo_id, advisory_id, reason, by_id) VALUES (?, ?, ?, ?)
			ON CONFLICT (repo_id, advisory_id) DO UPDATE SET reason = excluded.reason, by_id = excluded.by_id,
				created_at = strftime('%Y-%m-%dT%H:%M:%fZ','now')`,
			repoID, id, reason, byID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ReopenAdvisories takes dismissals back.
func (s *Store) ReopenAdvisories(repoID int64, ids []string) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, id := range ids {
		if _, err := tx.Exec("DELETE FROM dep_advisory_dismissals WHERE repo_id = ? AND advisory_id = ?", repoID, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// AdvisoryDismissals returns a repo's dismissals by advisory id.
func (s *Store) AdvisoryDismissals(repoID int64) (map[string]AdvisoryDismissal, error) {
	rows, err := s.DB.Query(`
		SELECT d.advisory_id, d.reason, COALESCE(u.username, ''), d.created_at
		FROM dep_advisory_dismissals d LEFT JOIN users u ON u.id = d.by_id
		WHERE d.repo_id = ?`, repoID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]AdvisoryDismissal{}
	for rows.Next() {
		var d AdvisoryDismissal
		if err := rows.Scan(&d.ID, &d.Reason, &d.By, &d.CreatedAt); err != nil {
			return nil, err
		}
		out[d.ID] = d
	}
	return out, rows.Err()
}
