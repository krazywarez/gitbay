package store

import (
	"database/sql"
	"errors"
)

// AddPageDomain claims a domain for a repo's pages site. The primary key
// makes claims exclusive instance-wide.
func (s *Store) AddPageDomain(domain string, repoID int64) error {
	_, err := s.DB.Exec("INSERT INTO page_domains (domain, repo_id) VALUES (?, ?)", domain, repoID)
	if err != nil && isUniqueErr(err) {
		return ErrExists
	}
	return err
}

func (s *Store) RemovePageDomain(domain string, repoID int64) error {
	res, err := s.DB.Exec("DELETE FROM page_domains WHERE domain = ? AND repo_id = ?", domain, repoID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) ListPageDomains(repoID int64) ([]string, error) {
	rows, err := s.DB.Query("SELECT domain FROM page_domains WHERE repo_id = ? ORDER BY domain", repoID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var d string
		if err := rows.Scan(&d); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// PageDomainRepo resolves a request host to the repo serving it.
func (s *Store) PageDomainRepo(domain string) (Repo, error) {
	var repoID int64
	err := s.DB.QueryRow("SELECT repo_id FROM page_domains WHERE domain = ?", domain).Scan(&repoID)
	if errors.Is(err, sql.ErrNoRows) {
		return Repo{}, ErrNotFound
	}
	if err != nil {
		return Repo{}, err
	}
	return s.RepoByID(repoID)
}
