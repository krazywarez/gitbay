package store

import (
	"errors"
	"time"
)

// CreateInvite stores an invite code hash bound to an email address.
func (s *Store) CreateInvite(codeHash, email string) error {
	_, err := s.DB.Exec("INSERT INTO invites (code_hash, email) VALUES (?, ?)", codeHash, email)
	return err
}

// ConsumeInvite redeems an invite exactly once, returning the address it was
// issued for. Used and unknown codes fail identically.
func (s *Store) ConsumeInvite(codeHash string) (string, error) {
	res, err := s.DB.Exec(
		"UPDATE invites SET used_at = strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE code_hash = ? AND used_at IS NULL",
		codeHash)
	if err != nil {
		return "", err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return "", ErrNotFound
	}
	var email string
	err = s.DB.QueryRow("SELECT email FROM invites WHERE code_hash = ?", codeHash).Scan(&email)
	return email, err
}

// CreateEmailToken stores a verification code hash for one address.
func (s *Store) CreateEmailToken(userID int64, address, tokenHash string, ttl time.Duration) error {
	_, err := s.DB.Exec(
		"INSERT INTO email_tokens (token_hash, user_id, address, expires_at) VALUES (?, ?, ?, ?)",
		tokenHash, userID, address, fmtTime(time.Now().Add(ttl)))
	return err
}

// ConsumeEmailToken redeems a verification code for the given user.
func (s *Store) ConsumeEmailToken(userID int64, tokenHash string) (string, error) {
	res, err := s.DB.Exec(`
		UPDATE email_tokens SET used_at = strftime('%Y-%m-%dT%H:%M:%fZ','now')
		WHERE token_hash = ? AND user_id = ? AND used_at IS NULL AND expires_at > ?`,
		tokenHash, userID, fmtTime(time.Now()))
	if err != nil {
		return "", err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return "", ErrNotFound
	}
	var address string
	err = s.DB.QueryRow("SELECT address FROM email_tokens WHERE token_hash = ?", tokenHash).Scan(&address)
	return address, err
}

// CreateRegisteredUser makes a self-registered account, pending until its
// email is verified.
func (s *Store) CreateRegisteredUser(username string, pending bool) (int64, error) {
	res, err := s.DB.Exec("INSERT INTO users (username, pending) VALUES (?, ?)", username, boolInt(pending))
	if err != nil {
		if isUniqueErr(err) {
			return 0, errors.New("that username is taken")
		}
		return 0, err
	}
	return res.LastInsertId()
}

// ClearPending activates a pending account.
func (s *Store) ClearPending(userID int64) error {
	_, err := s.DB.Exec("UPDATE users SET pending = 0 WHERE id = ?", userID)
	return err
}
