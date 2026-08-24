package store

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

type APIToken struct {
	Name       string
	Scope      string
	CreatedAt  string
	ExpiresAt  *time.Time
	LastUsedAt *time.Time
}

// CreateAPIToken stores a token hash; expires nil means no expiry.
func (s *Store) CreateAPIToken(userID int64, name, tokenHash, scope string, expires *time.Time) error {
	var exp any
	if expires != nil {
		exp = fmtTime(*expires)
	}
	_, err := s.DB.Exec(
		"INSERT INTO api_tokens (user_id, name, token_hash, scope, expires_at) VALUES (?, ?, ?, ?, ?)",
		userID, name, tokenHash, scope, exp)
	if isUniqueErr(err) {
		return fmt.Errorf("you already have a token named %q", name)
	}
	return err
}

// APITokenUser resolves a presented token to its user and scope; expired and
// unknown tokens fail identically.
func (s *Store) APITokenUser(tokenHash string) (User, string, error) {
	var userID int64
	var scope string
	err := s.DB.QueryRow(`
		SELECT user_id, scope FROM api_tokens
		WHERE token_hash = ? AND (expires_at IS NULL OR expires_at > ?)`,
		tokenHash, fmtTime(time.Now())).Scan(&userID, &scope)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, "", ErrNotFound
	}
	if err != nil {
		return User{}, "", err
	}
	s.DB.Exec("UPDATE api_tokens SET last_used_at = strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE token_hash = ?", tokenHash)
	u, err := s.UserByID(userID)
	return u, scope, err
}

func (s *Store) ListAPITokens(userID int64) ([]APIToken, error) {
	rows, err := s.DB.Query(
		"SELECT name, scope, created_at, expires_at, last_used_at FROM api_tokens WHERE user_id = ? ORDER BY name", userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []APIToken
	for rows.Next() {
		var t APIToken
		var exp, used sql.NullString
		if err := rows.Scan(&t.Name, &t.Scope, &t.CreatedAt, &exp, &used); err != nil {
			return nil, err
		}
		t.ExpiresAt = parseTime(exp)
		t.LastUsedAt = parseTime(used)
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) RevokeAPIToken(userID int64, name string) error {
	res, err := s.DB.Exec("DELETE FROM api_tokens WHERE user_id = ? AND name = ?", userID, name)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
