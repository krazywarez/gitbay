package store

import (
	"database/sql"
	"errors"
)

// SecurityScan is a repo's code scanning state. A row exists only while
// scanning is enabled.
type SecurityScan struct {
	RepoID      int64
	NextRun     string
	LastResults string // when results were last uploaded, '' for never
	IssueNumber int64
	Aside       string // what the last scan set aside, as JSON
}

// SecurityFinding is one result from a scan, as last reported.
type SecurityFinding struct {
	Fingerprint string
	Tool        string
	Rule        string
	Level       string
	Path        string
	Line        int
	Message     string
}

func (s *Store) EnableSecurityScan(repoID int64, nextRun string) error {
	_, err := s.DB.Exec("INSERT OR IGNORE INTO security_scans (repo_id, next_run) VALUES (?, ?)", repoID, nextRun)
	return err
}

// DisableSecurityScan stops scanning and forgets what was reported.
func (s *Store) DisableSecurityScan(repoID int64) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec("DELETE FROM security_findings WHERE repo_id = ?", repoID); err != nil {
		return err
	}
	if _, err := tx.Exec("DELETE FROM security_scans WHERE repo_id = ?", repoID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) SecurityScanFor(repoID int64) (SecurityScan, error) {
	var sc SecurityScan
	err := s.DB.QueryRow(
		"SELECT repo_id, next_run, last_results, issue_number, aside FROM security_scans WHERE repo_id = ?", repoID).
		Scan(&sc.RepoID, &sc.NextRun, &sc.LastResults, &sc.IssueNumber, &sc.Aside)
	if errors.Is(err, sql.ErrNoRows) {
		return sc, ErrNotFound
	}
	return sc, err
}

// DueSecurityScans returns the repos whose next scan is at or before now.
// Archived repos are skipped: nobody is going to act on the findings.
func (s *Store) DueSecurityScans(nowISO string) ([]Repo, error) {
	rows, err := s.DB.Query(repoSelect+`
		JOIN security_scans sc ON sc.repo_id = r.id
		WHERE sc.next_run <= ?
		ORDER BY sc.next_run, r.id`, nowISO)
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

func (s *Store) SetSecurityNext(repoID int64, nextRun string) error {
	_, err := s.DB.Exec("UPDATE security_scans SET next_run = ? WHERE repo_id = ?", nextRun, repoID)
	return err
}

func (s *Store) SetSecurityIssue(repoID, number int64) error {
	_, err := s.DB.Exec("UPDATE security_scans SET issue_number = ? WHERE repo_id = ?", number, repoID)
	return err
}

func (s *Store) ReportedFindings(repoID int64) ([]SecurityFinding, error) {
	rows, err := s.DB.Query(`
		SELECT fingerprint, tool, rule, level, path, line, message FROM security_findings
		WHERE repo_id = ? ORDER BY fingerprint`, repoID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SecurityFinding
	for rows.Next() {
		var f SecurityFinding
		if err := rows.Scan(&f.Fingerprint, &f.Tool, &f.Rule, &f.Level, &f.Path, &f.Line, &f.Message); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// ReplaceFindings swaps in an upload's findings wholesale, with what it set
// aside.
func (s *Store) ReplaceFindings(repoID int64, findings []SecurityFinding, aside string) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec("UPDATE security_scans SET aside = ? WHERE repo_id = ?", aside, repoID); err != nil {
		return err
	}
	if _, err := tx.Exec("DELETE FROM security_findings WHERE repo_id = ?", repoID); err != nil {
		return err
	}
	for _, f := range findings {
		if _, err := tx.Exec(`
			INSERT OR IGNORE INTO security_findings (repo_id, fingerprint, tool, rule, level, path, line, message)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			repoID, f.Fingerprint, f.Tool, f.Rule, f.Level, f.Path, f.Line, f.Message); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// StampSecurityResults records that a scan's results arrived.
func (s *Store) StampSecurityResults(repoID int64) error {
	_, err := s.DB.Exec(`UPDATE security_scans SET last_results = strftime('%Y-%m-%dT%H:%M:%fZ','now')
		WHERE repo_id = ?`, repoID)
	return err
}

// SecurityDismissal is a finding a repository dismissed.
type SecurityDismissal struct {
	Fingerprint string
	Reason      string
	By          string // username, '' once the account is gone
	CreatedAt   string
}

// DismissFindings dismisses findings by fingerprint, replacing the reason
// of any already dismissed.
func (s *Store) DismissFindings(repoID, byID int64, fingerprints []string, reason string) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, fp := range fingerprints {
		if _, err := tx.Exec(`
			INSERT INTO security_dismissals (repo_id, fingerprint, reason, by_id) VALUES (?, ?, ?, ?)
			ON CONFLICT (repo_id, fingerprint) DO UPDATE SET reason = excluded.reason, by_id = excluded.by_id,
				created_at = strftime('%Y-%m-%dT%H:%M:%fZ','now')`,
			repoID, fp, reason, byID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ReopenFindings takes dismissals back.
func (s *Store) ReopenFindings(repoID int64, fingerprints []string) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, fp := range fingerprints {
		if _, err := tx.Exec("DELETE FROM security_dismissals WHERE repo_id = ? AND fingerprint = ?", repoID, fp); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// SecurityDismissals returns a repo's dismissals by fingerprint.
func (s *Store) SecurityDismissals(repoID int64) (map[string]SecurityDismissal, error) {
	rows, err := s.DB.Query(`
		SELECT d.fingerprint, d.reason, COALESCE(u.username, ''), d.created_at
		FROM security_dismissals d LEFT JOIN users u ON u.id = d.by_id
		WHERE d.repo_id = ?`, repoID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]SecurityDismissal{}
	for rows.Next() {
		var d SecurityDismissal
		if err := rows.Scan(&d.Fingerprint, &d.Reason, &d.By, &d.CreatedAt); err != nil {
			return nil, err
		}
		out[d.Fingerprint] = d
	}
	return out, rows.Err()
}
