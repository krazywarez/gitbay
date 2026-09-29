package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

type APIToken struct {
	ID         int64
	Name       string
	Scope      string
	CreatedAt  string
	ExpiresAt  *time.Time
	LastUsedAt *time.Time
	CreatedBy  string // name of the token that created this one; "" for none
}

// nullID stores 0 as NULL.
func nullID(id int64) any {
	if id == 0 {
		return nil
	}
	return id
}

// CreateAPIToken stores a token hash; expires nil means no expiry,
// createdByToken 0 means it was not created through a token.
func (s *Store) CreateAPIToken(userID int64, name, tokenHash, scope string, expires *time.Time, createdByToken int64) error {
	var exp any
	if expires != nil {
		exp = fmtTime(*expires)
	}
	_, err := s.DB.Exec(
		"INSERT INTO api_tokens (user_id, name, token_hash, scope, expires_at, created_by_token) VALUES (?, ?, ?, ?, ?, ?)",
		userID, name, tokenHash, scope, exp, nullID(createdByToken))
	if isUniqueErr(err) {
		return fmt.Errorf("you already have a token named %q", name)
	}
	return err
}

// APITokenUser resolves a presented token to its user and the token;
// expired and unknown tokens fail identically.
func (s *Store) APITokenUser(tokenHash string) (User, APIToken, error) {
	var userID int64
	var t APIToken
	var exp sql.NullString
	err := s.DB.QueryRow(`
		SELECT user_id, id, name, scope, expires_at FROM api_tokens
		WHERE token_hash = ? AND (expires_at IS NULL OR expires_at > ?)`,
		tokenHash, fmtTime(time.Now())).Scan(&userID, &t.ID, &t.Name, &t.Scope, &exp)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, APIToken{}, ErrNotFound
	}
	if err != nil {
		return User{}, APIToken{}, err
	}
	t.ExpiresAt = parseTime(exp)
	s.DB.Exec("UPDATE api_tokens SET last_used_at = strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE token_hash = ?", tokenHash)
	u, err := s.UserByID(userID)
	return u, t, err
}

// APITokenByID loads a token by id, expired or not.
func (s *Store) APITokenByID(id int64) (APIToken, error) {
	var t APIToken
	var exp sql.NullString
	err := s.DB.QueryRow("SELECT id, name, scope, created_at, expires_at FROM api_tokens WHERE id = ?", id).
		Scan(&t.ID, &t.Name, &t.Scope, &t.CreatedAt, &exp)
	if errors.Is(err, sql.ErrNoRows) {
		return t, ErrNotFound
	}
	t.ExpiresAt = parseTime(exp)
	return t, err
}

func (s *Store) ListAPITokens(userID int64) ([]APIToken, error) {
	rows, err := s.DB.Query(`
		SELECT t.id, t.name, t.scope, t.created_at, t.expires_at, t.last_used_at, COALESCE(p.name, '')
		FROM api_tokens t LEFT JOIN api_tokens p ON p.id = t.created_by_token
		WHERE t.user_id = ? ORDER BY t.name`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []APIToken
	for rows.Next() {
		var t APIToken
		var exp, used sql.NullString
		if err := rows.Scan(&t.ID, &t.Name, &t.Scope, &t.CreatedAt, &exp, &used, &t.CreatedBy); err != nil {
			return nil, err
		}
		t.ExpiresAt = parseTime(exp)
		t.LastUsedAt = parseTime(used)
		out = append(out, t)
	}
	return out, rows.Err()
}

// Created is what a token made, directly or through tokens it made:
// token names and SSH key fingerprints.
type Created struct {
	Tokens []string `json:"tokens"`
	Keys   []string `json:"keys"`
}

// chainCTE selects the token named by the first argument and every
// token created from it, at any depth.
const chainCTE = `WITH RECURSIVE chain(id) AS (
	SELECT ? UNION SELECT t.id FROM api_tokens t JOIN chain ON t.created_by_token = chain.id)`

// RevokeAPIToken deletes the user's token by name and returns what it
// created. withCreated deletes those too; otherwise they stay and lose
// the link to the revoked token.
func (s *Store) RevokeAPIToken(userID int64, name string, withCreated bool) (Created, error) {
	tx, err := s.DB.Begin()
	if err != nil {
		return Created{}, err
	}
	defer tx.Rollback()
	var id int64
	err = tx.QueryRow("SELECT id FROM api_tokens WHERE user_id = ? AND name = ?", userID, name).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return Created{}, ErrNotFound
	}
	if err != nil {
		return Created{}, err
	}
	var c Created
	rows, err := tx.Query(chainCTE+` SELECT name FROM api_tokens WHERE id IN (SELECT id FROM chain) AND id != ? ORDER BY name`, id, id)
	if err != nil {
		return Created{}, err
	}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			rows.Close()
			return Created{}, err
		}
		c.Tokens = append(c.Tokens, n)
	}
	rows.Close()
	var keyIDs []int64
	rows, err = tx.Query(chainCTE+` SELECT id, fingerprint FROM ssh_keys WHERE created_by_token IN (SELECT id FROM chain) ORDER BY id`, id)
	if err != nil {
		return Created{}, err
	}
	for rows.Next() {
		var kid int64
		var fp string
		if err := rows.Scan(&kid, &fp); err != nil {
			rows.Close()
			return Created{}, err
		}
		keyIDs = append(keyIDs, kid)
		c.Keys = append(c.Keys, fp)
	}
	rows.Close()

	if !withCreated {
		if _, err := tx.Exec("DELETE FROM api_tokens WHERE id = ?", id); err != nil {
			return Created{}, err
		}
		return c, tx.Commit()
	}
	if len(keyIDs) > 0 {
		args := make([]any, len(keyIDs))
		for i, k := range keyIDs {
			args[i] = k
		}
		if _, err := tx.Exec("DELETE FROM ssh_keys WHERE id IN (?"+strings.Repeat(", ?", len(keyIDs)-1)+")", args...); err != nil {
			return Created{}, err
		}
		if err := bumpKeyEpoch(tx); err != nil {
			return Created{}, err
		}
	}
	if _, err := tx.Exec(chainCTE+` DELETE FROM api_tokens WHERE id IN (SELECT id FROM chain)`, id); err != nil {
		return Created{}, err
	}
	if err := tx.Commit(); err != nil {
		return Created{}, err
	}
	if len(keyIDs) > 0 {
		s.announce(Revoked{KeyIDs: keyIDs})
	}
	return c, nil
}
