package store

import (
	"database/sql"
	"errors"
)

// PageDomain is one custom-domain claim. A claim starts pending — it holds
// the domain but serves nothing — and activates when the DNS challenge
// verifies. Pending claims expire so a squatted claim frees itself.
type PageDomain struct {
	Domain     string
	RepoID     int64
	UserID     int64
	Token      string
	CreatedAt  string
	VerifiedAt string
}

func (d PageDomain) Verified() bool { return d.VerifiedAt != "" }

// AddPageDomain claims a domain for a repo. Expired pending claims (any
// repo's) are cleared first, so abandonment frees the name; live claims
// make the insert fail with ErrExists.
func (s *Store) AddPageDomain(domain string, repoID, userID int64, token string, ttlSeconds int) error {
	if _, err := s.DB.Exec(
		"DELETE FROM page_domains WHERE domain = ? AND verified_at = '' AND strftime('%s','now') - strftime('%s', created_at) > ?",
		domain, ttlSeconds); err != nil {
		return err
	}
	_, err := s.DB.Exec(
		"INSERT INTO page_domains (domain, repo_id, user_id, token) VALUES (?, ?, ?, ?)",
		domain, repoID, userID, token)
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

const pageDomainSelect = "SELECT domain, repo_id, user_id, token, created_at, verified_at FROM page_domains"

func scanPageDomain(row interface{ Scan(...any) error }) (PageDomain, error) {
	var d PageDomain
	err := row.Scan(&d.Domain, &d.RepoID, &d.UserID, &d.Token, &d.CreatedAt, &d.VerifiedAt)
	return d, err
}

// PageDomainClaim returns a repo's claim on a domain, verified or pending.
func (s *Store) PageDomainClaim(domain string, repoID int64) (PageDomain, error) {
	d, err := scanPageDomain(s.DB.QueryRow(
		pageDomainSelect+" WHERE domain = ? AND repo_id = ?", domain, repoID))
	if errors.Is(err, sql.ErrNoRows) {
		return d, ErrNotFound
	}
	return d, err
}

// PageDomainExpired reports whether a pending claim has outlived the TTL.
func (s *Store) PageDomainExpired(d PageDomain, ttlSeconds int) bool {
	if d.Verified() {
		return false
	}
	var expired bool
	s.DB.QueryRow(
		"SELECT strftime('%s','now') - strftime('%s', ?) > ?", d.CreatedAt, ttlSeconds).Scan(&expired)
	return expired
}

// VerifyPageDomain activates a pending claim.
func (s *Store) VerifyPageDomain(domain string, repoID int64) error {
	res, err := s.DB.Exec(
		"UPDATE page_domains SET verified_at = strftime('%Y-%m-%dT%H:%M:%SZ','now') WHERE domain = ? AND repo_id = ?",
		domain, repoID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) ListPageDomains(repoID int64) ([]PageDomain, error) {
	rows, err := s.DB.Query(pageDomainSelect+" WHERE repo_id = ? ORDER BY domain", repoID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PageDomain
	for rows.Next() {
		d, err := scanPageDomain(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// PageDomainRepo resolves a request host to the repo serving it. Only
// verified claims serve.
func (s *Store) PageDomainRepo(domain string) (Repo, error) {
	var repoID int64
	err := s.DB.QueryRow(
		"SELECT repo_id FROM page_domains WHERE domain = ? AND verified_at != ''", domain).Scan(&repoID)
	if errors.Is(err, sql.ErrNoRows) {
		return Repo{}, ErrNotFound
	}
	if err != nil {
		return Repo{}, err
	}
	return s.RepoByID(repoID)
}
