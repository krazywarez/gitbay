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
	CreatedAt   string
	LastUsedAt  string // "" when the key has never authenticated
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

// DeleteUser removes an account whose removal orphans nothing: no owned
// repositories, no authored issues, MRs, comments, or reviews, and not the
// only admin of an org. Everything else (keys, emails, sessions, tokens,
// pins, memberships, activity) cascades. Blockers come back as an error
// naming what stands in the way, so the operator can transfer, delete, or
// disable instead.
func (s *Store) DeleteUser(id int64) error {
	var blockers []string
	var checkErr error
	count := func(q string, what string) {
		var n int
		if err := s.DB.QueryRow(q, id).Scan(&n); err != nil {
			if checkErr == nil {
				checkErr = fmt.Errorf("checking %s: %w", what, err)
			}
			return
		}
		if n > 0 {
			blockers = append(blockers, fmt.Sprintf("%d %s", n, what))
		}
	}
	count("SELECT COUNT(*) FROM repos WHERE owner_kind = 'user' AND owner_id = ?", "owned repositories")
	count("SELECT COUNT(*) FROM issues WHERE author_id = ?", "authored issues")
	count("SELECT COUNT(*) FROM merge_requests WHERE author_id = ?", "authored merge requests")
	count("SELECT COUNT(*) FROM issue_comments WHERE author_id = ?", "issue comments")
	count("SELECT COUNT(*) FROM mr_comments WHERE author_id = ?", "MR comments")
	count("SELECT COUNT(*) FROM mr_diff_comments WHERE author_id = ?", "diff comments")
	count("SELECT COUNT(*) FROM mr_reviews WHERE reviewer_id = ?", "reviews")
	count(`SELECT COUNT(*) FROM org_members m WHERE m.user_id = ? AND m.role = 'admin'
		AND NOT EXISTS (SELECT 1 FROM org_members o
			WHERE o.org_id = m.org_id AND o.role = 'admin' AND o.user_id != m.user_id)`,
		"organizations with no other admin")
	if checkErr != nil {
		return checkErr
	}
	if len(blockers) > 0 {
		return fmt.Errorf("account still anchors: %s — transfer or delete those first, or disable the account instead",
			strings.Join(blockers, ", "))
	}
	res, err := s.DB.Exec("DELETE FROM users WHERE id = ?", id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// OwnerExists reports whether a user or org owns the name — the ACME host
// policy check for pages subdomains.
func (s *Store) OwnerExists(name string) bool {
	var n int
	s.DB.QueryRow(`SELECT (SELECT COUNT(*) FROM users WHERE username = ?1)
		+ (SELECT COUNT(*) FROM orgs WHERE name = ?1)`, name).Scan(&n)
	return n > 0
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

// UserEmailAddresses returns every address on the account, verified or not.
func (s *Store) UserEmailAddresses(userID int64) ([]string, error) {
	rows, err := s.DB.Query("SELECT address FROM emails WHERE user_id = ? ORDER BY is_primary DESC, address", userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var a string
		if err := rows.Scan(&a); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// Email is one address on an account, with the state the signature rules
// and notification routing depend on.
type Email struct {
	Address    string
	Verified   bool
	VerifiedBy string // smtp | admin, empty when unverified
	Primary    bool
}

// ListEmails returns every address on the account with its state.
func (s *Store) ListEmails(userID int64) ([]Email, error) {
	rows, err := s.DB.Query(`SELECT address, verified_at IS NOT NULL,
		COALESCE(verified_by, ''), is_primary
		FROM emails WHERE user_id = ? ORDER BY is_primary DESC, address`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Email
	for rows.Next() {
		var e Email
		if err := rows.Scan(&e.Address, &e.Verified, &e.VerifiedBy, &e.Primary); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// SetUserDisabled suspends or restores an account. Disabling drops every
// credential that would grant a session on its own — web sessions, API
// tokens, unclaimed login links — and leaves the SSH keys registered but
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
		// A pending login link is a session in waiting, so it goes with
		// the sessions and API tokens. Re-enabling means minting again.
		for _, table := range []string{"web_sessions", "api_tokens", "login_tokens"} {
			if _, err = s.DB.Exec("DELETE FROM "+table+" WHERE user_id = ?", userID); err != nil {
				return err
			}
		}
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
		`SELECT id, user_id, fingerprint, algo, blob, scope, created_at, COALESCE(last_used_at, '')
		 FROM ssh_keys WHERE user_id = ? ORDER BY id`,
		userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var keys []SSHKey
	for rows.Next() {
		var k SSHKey
		if err := rows.Scan(&k.ID, &k.UserID, &k.Fingerprint, &k.Algo, &k.Blob, &k.Scope, &k.CreatedAt, &k.LastUsedAt); err != nil {
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
