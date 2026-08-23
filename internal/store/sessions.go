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
