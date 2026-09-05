package store

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"time"
)

// NewToken returns a fresh random token and its storage hash. Only the hash
// is persisted; the token itself goes to the user once.
func NewToken() (token, hash string, err error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", "", err
	}
	token = hex.EncodeToString(b[:])
	return token, HashToken(token), nil
}

func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func fmtTime(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }

// CreateLoginToken stores a one-time login token hash.
func (s *Store) CreateLoginToken(userID int64, hash string, ttl time.Duration) error {
	_, err := s.DB.Exec(
		"INSERT INTO login_tokens (token_hash, user_id, expires_at) VALUES (?, ?, ?)",
		hash, userID, fmtTime(time.Now().Add(ttl)))
	return err
}

// CountLoginTokensSince counts the login tokens minted for a user within a
// window. An unauthenticated request can ask for a login link, so the mint
// needs a durable per-account bound the way email verification does (#136).
func (s *Store) CountLoginTokensSince(userID int64, since time.Time) (int, error) {
	var n int
	err := s.DB.QueryRow(
		"SELECT count(*) FROM login_tokens WHERE user_id = ? AND created_at > ?",
		userID, fmtTime(since)).Scan(&n)
	return n, err
}

// ConsumeLoginToken redeems a token exactly once; expired or used tokens
// fail identically.
func (s *Store) ConsumeLoginToken(hash string) (int64, error) {
	res, err := s.DB.Exec(`
		UPDATE login_tokens SET used_at = strftime('%Y-%m-%dT%H:%M:%fZ','now')
		WHERE token_hash = ? AND used_at IS NULL AND expires_at > ?`,
		hash, fmtTime(time.Now()))
	if err != nil {
		return 0, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return 0, ErrNotFound
	}
	var userID int64
	err = s.DB.QueryRow("SELECT user_id FROM login_tokens WHERE token_hash = ?", hash).Scan(&userID)
	return userID, err
}

func (s *Store) CreateWebSession(hash string, userID int64, ttl time.Duration) error {
	_, err := s.DB.Exec(
		"INSERT INTO web_sessions (token_hash, user_id, expires_at) VALUES (?, ?, ?)",
		hash, userID, fmtTime(time.Now().Add(ttl)))
	return err
}

// WebSessionUser resolves a session cookie hash to its user.
func (s *Store) WebSessionUser(hash string) (User, error) {
	var userID int64
	err := s.DB.QueryRow(
		"SELECT user_id FROM web_sessions WHERE token_hash = ? AND expires_at > ?",
		hash, fmtTime(time.Now())).Scan(&userID)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrNotFound
	}
	if err != nil {
		return User{}, err
	}
	return s.UserByID(userID)
}

func (s *Store) DeleteWebSession(hash string) error {
	_, err := s.DB.Exec("DELETE FROM web_sessions WHERE token_hash = ?", hash)
	return err
}

// WebSession is one browser session as its owner lists it. ID is the first
// twelve hex digits of the stored token hash: enough to name it, and a
// hash of the cookie rather than the cookie.
type WebSession struct {
	ID        string `json:"id"`
	CreatedAt string `json:"created_at"`
	ExpiresAt string `json:"expires_at"`
}

// ListWebSessions lists the user's unexpired browser sessions, newest first.
func (s *Store) ListWebSessions(userID int64) ([]WebSession, error) {
	rows, err := s.DB.Query(`SELECT substr(token_hash, 1, 12), created_at, expires_at
		FROM web_sessions WHERE user_id = ? AND expires_at > ? ORDER BY created_at DESC`,
		userID, fmtTime(time.Now()))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []WebSession
	for rows.Next() {
		var ws WebSession
		if err := rows.Scan(&ws.ID, &ws.CreatedAt, &ws.ExpiresAt); err != nil {
			return nil, err
		}
		out = append(out, ws)
	}
	return out, rows.Err()
}

// RevokeWebSession ends one of the user's sessions by its listed id.
func (s *Store) RevokeWebSession(userID int64, id string) error {
	res, err := s.DB.Exec("DELETE FROM web_sessions WHERE user_id = ? AND substr(token_hash, 1, 12) = ?", userID, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// RevokeAllWebSessions ends every browser session the user has.
func (s *Store) RevokeAllWebSessions(userID int64) (int64, error) {
	res, err := s.DB.Exec("DELETE FROM web_sessions WHERE user_id = ?", userID)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
