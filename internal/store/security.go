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
		"SELECT repo_id, next_run, last_results, issue_number FROM security_scans WHERE repo_id = ?", repoID).
		Scan(&sc.RepoID, &sc.NextRun, &sc.LastResults, &sc.IssueNumber)
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

// ReplaceFindings swaps in an upload's findings wholesale.
func (s *Store) ReplaceFindings(repoID int64, findings []SecurityFinding) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
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
