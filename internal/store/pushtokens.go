package store

import (
	"database/sql"
	"errors"
	"time"
)

// PushToken is the receive-pack a hook request speaks for.
type PushToken struct {
	RepoID int64
	UserID int64
	Scope  string
}

// pushTokenTTL bounds a row whose receive-pack died before deleting it.
const pushTokenTTL = 24 * time.Hour

// CreatePushToken records a token for one receive-pack and returns it.
func (s *Store) CreatePushToken(repoID, userID int64, scope string) (string, error) {
	token, hash, err := NewToken()
	if err != nil {
		return "", err
	}
	_, err = s.DB.Exec(
		"INSERT INTO push_tokens (token_hash, repo_id, user_id, scope, expires_at) VALUES (?, ?, ?, ?, ?)",
		hash, repoID, userID, scope, fmtTime(time.Now().Add(pushTokenTTL)))
	if err != nil {
		return "", err
	}
	return token, nil
}

// PushTokenByHash looks up a live token by its stored hash. ErrNotFound
// covers both an absent row and one that has expired.
func (s *Store) PushTokenByHash(hash string) (PushToken, error) {
	var t PushToken
	err := s.DB.QueryRow(
		"SELECT repo_id, user_id, scope FROM push_tokens WHERE token_hash = ? AND expires_at > ?",
		hash, fmtTime(time.Now())).Scan(&t.RepoID, &t.UserID, &t.Scope)
	if errors.Is(err, sql.ErrNoRows) {
		return PushToken{}, ErrNotFound
	}
	return t, err
}

// DeletePushToken removes a token by its raw value, once its receive-pack
// is done with it.
func (s *Store) DeletePushToken(token string) error {
	_, err := s.DB.Exec("DELETE FROM push_tokens WHERE token_hash = ?", HashToken(token))
	return err
}
