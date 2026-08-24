package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

type User struct {
	ID       int64
	Username string
	IsAdmin  bool
	Pending  bool // self-registered, email not yet verified
	Disabled bool // administratively suspended
}

type SSHKey struct {
	ID          int64
	UserID      int64
	Fingerprint string
	Algo        string
	Blob        []byte
	Scope       string
}

// ErrDuplicateKey carries the exact user-facing message from the spec. It
// deliberately does not name the owning account (enumeration oracle).
var ErrDuplicateKey = errors.New("that key is already registered to another account; remove it there first or use a different key")

var ErrNotFound = errors.New("not found")

func (s *Store) CreateUser(username string, isAdmin bool) (int64, error) {
	if taken, err := ownerNameTaken(s.DB, username); err != nil {
		return 0, err
	} else if taken {
		return 0, fmt.Errorf("username %q is taken", username)
	}
	res, err := s.DB.Exec("INSERT INTO users (username, is_admin) VALUES (?, ?)", username, boolInt(isAdmin))
	if err != nil {
		if isUniqueErr(err) {
			return 0, fmt.Errorf("username %q is taken", username)
		}
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) UserByUsername(name string) (User, error) {
	var u User
	var admin, pending, disabled int
	err := s.DB.QueryRow("SELECT id, username, is_admin, pending, disabled FROM users WHERE username = ?", name).
		Scan(&u.ID, &u.Username, &admin, &pending, &disabled)
	if errors.Is(err, sql.ErrNoRows) {
		return u, ErrNotFound
	}
	u.IsAdmin = admin != 0
	u.Pending = pending != 0
	u.Disabled = disabled != 0
	return u, err
}

// SetUserDisabled suspends or restores an account. Disabling also drops
// the user's web sessions; their keys and tokens stay registered but are
// refused at every entry point until re-enabled.
func (s *Store) SetUserDisabled(userID int64, disabled bool) error {
	v := 0
	if disabled {
		v = 1
	}
	res, err := s.DB.Exec("UPDATE users SET disabled = ? WHERE id = ?", v, userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	if disabled {
		_, err = s.DB.Exec("DELETE FROM web_sessions WHERE user_id = ?", userID)
	}
	return err
}

func (s *Store) UserByID(id int64) (User, error) {
	var u User
	var admin, pending, disabled int
	err := s.DB.QueryRow("SELECT id, username, is_admin, pending, disabled FROM users WHERE id = ?", id).
		Scan(&u.ID, &u.Username, &admin, &pending, &disabled)
	if errors.Is(err, sql.ErrNoRows) {
		return u, ErrNotFound
	}
	u.IsAdmin = admin != 0
	u.Pending = pending != 0
	u.Disabled = disabled != 0
	return u, err
}

// AddSSHKey registers a key and bumps the key epoch in one transaction.
func (s *Store) AddSSHKey(userID int64, fingerprint, algo string, blob []byte, scope string) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(
		"INSERT INTO ssh_keys (user_id, fingerprint, algo, blob, scope) VALUES (?, ?, ?, ?, ?)",
		userID, fingerprint, algo, blob, scope); err != nil {
		if isUniqueErr(err) {
			return ErrDuplicateKey
		}
		return err
	}
	if err := bumpKeyEpoch(tx); err != nil {
		return err
	}
	return tx.Commit()
}

// RemoveSSHKey removes a key owned by userID and bumps the key epoch.
func (s *Store) RemoveSSHKey(userID int64, fingerprint string) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.Exec("DELETE FROM ssh_keys WHERE user_id = ? AND fingerprint = ?", userID, fingerprint)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	if err := bumpKeyEpoch(tx); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) SSHKeyByFingerprint(fingerprint string) (SSHKey, error) {
	var k SSHKey
	err := s.DB.QueryRow(
		"SELECT id, user_id, fingerprint, algo, blob, scope FROM ssh_keys WHERE fingerprint = ?",
		fingerprint).Scan(&k.ID, &k.UserID, &k.Fingerprint, &k.Algo, &k.Blob, &k.Scope)
	if errors.Is(err, sql.ErrNoRows) {
		return k, ErrNotFound
	}
	return k, err
}

func (s *Store) ListSSHKeys(userID int64) ([]SSHKey, error) {
	rows, err := s.DB.Query(
		"SELECT id, user_id, fingerprint, algo, blob, scope FROM ssh_keys WHERE user_id = ? ORDER BY id",
		userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var keys []SSHKey
	for rows.Next() {
		var k SSHKey
		if err := rows.Scan(&k.ID, &k.UserID, &k.Fingerprint, &k.Algo, &k.Blob, &k.Scope); err != nil {
			return nil, err
		}
		keys = append(keys, k)
	}
	return keys, rows.Err()
}

// TouchSSHKey records key use; best-effort, callers ignore the error.
func (s *Store) TouchSSHKey(id int64) error {
	_, err := s.DB.Exec(
		"UPDATE ssh_keys SET last_used_at = strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id = ?", id)
	return err
}

// AddEmail adds an address; verifiedBy is "" (unverified), "smtp", or "admin".
// Adding an already-verified address bumps the key epoch: it is a trust input
// for signature states.
func (s *Store) AddEmail(userID int64, address, verifiedBy string, primary bool) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var vAt, vBy any
	if verifiedBy != "" {
		vAt = "now"
		vBy = verifiedBy
	}
	_, err = tx.Exec(
		`INSERT INTO emails (user_id, address, verified_at, verified_by, is_primary)
		 VALUES (?, ?, CASE WHEN ? IS NULL THEN NULL ELSE strftime('%Y-%m-%dT%H:%M:%fZ','now') END, ?, ?)`,
		userID, address, vAt, vBy, boolInt(primary))
	if isUniqueErr(err) {
		return fmt.Errorf("address %q is already in use", address)
	}
	if err != nil {
		return err
	}
	if verifiedBy != "" {
		if err := bumpKeyEpoch(tx); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) KeyEpoch() (int64, error) {
	var v int64
	err := s.DB.QueryRow("SELECT value FROM settings WHERE key = 'key_epoch'").Scan(&v)
	return v, err
}

type execer interface {
	Exec(query string, args ...any) (sql.Result, error)
}

func bumpKeyEpoch(tx execer) error {
	_, err := tx.Exec("UPDATE settings SET value = value + 1 WHERE key = 'key_epoch'")
	return err
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func isUniqueErr(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}

func (s *Store) SSHKeyByID(id int64) (SSHKey, error) {
	var k SSHKey
	err := s.DB.QueryRow(
		"SELECT id, user_id, fingerprint, algo, blob, scope FROM ssh_keys WHERE id = ?",
		id).Scan(&k.ID, &k.UserID, &k.Fingerprint, &k.Algo, &k.Blob, &k.Scope)
	if errors.Is(err, sql.ErrNoRows) {
		return k, ErrNotFound
	}
	return k, err
}

// ListDeployKeys returns the deploy keys bound to a repository.
func (s *Store) ListDeployKeys(repoID int64) ([]SSHKey, error) {
	rows, err := s.DB.Query(
		"SELECT id, user_id, fingerprint, algo, blob, scope FROM ssh_keys WHERE scope LIKE 'deploy:' || ? || ':%' ORDER BY id",
		repoID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var keys []SSHKey
	for rows.Next() {
		var k SSHKey
		if err := rows.Scan(&k.ID, &k.UserID, &k.Fingerprint, &k.Algo, &k.Blob, &k.Scope); err != nil {
			return nil, err
		}
		keys = append(keys, k)
	}
	return keys, rows.Err()
}

// RemoveDeployKey removes a deploy key from a repository by fingerprint;
// any repo admin may remove it regardless of who added it.
func (s *Store) RemoveDeployKey(repoID int64, fingerprint string) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.Exec(
		"DELETE FROM ssh_keys WHERE fingerprint = ? AND scope LIKE 'deploy:' || ? || ':%'",
		fingerprint, repoID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	if err := bumpKeyEpoch(tx); err != nil {
		return err
	}
	return tx.Commit()
}
