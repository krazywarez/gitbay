package store

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// ErrGhostNameTaken is returned when a real account holds the name the
// ghost needs.
var ErrGhostNameTaken = errors.New(`an account named "ghost" exists and is not the ghost; rename it before an account can be deleted`)

// RequestAccountDeletion records a deletion request waiting on its mailed
// link. A second request replaces the first.
func (s *Store) RequestAccountDeletion(userID int64, tokenHash string, ttl time.Duration) error {
	_, err := s.DB.Exec(`INSERT INTO account_deletions (user_id, token_hash, expires_at) VALUES (?, ?, ?)
		ON CONFLICT (user_id) DO UPDATE SET token_hash = excluded.token_hash,
			created_at = strftime('%Y-%m-%dT%H:%M:%fZ','now'), expires_at = excluded.expires_at`,
		userID, tokenHash, fmtTime(time.Now().Add(ttl)))
	return err
}

// AccountDeletionUser is the account an unexpired deletion link names.
func (s *Store) AccountDeletionUser(tokenHash string) (User, error) {
	var userID int64
	err := s.DB.QueryRow("SELECT user_id FROM account_deletions WHERE token_hash = ? AND expires_at > ?",
		tokenHash, fmtTime(time.Now())).Scan(&userID)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrNotFound
	}
	if err != nil {
		return User{}, err
	}
	return s.UserByID(userID)
}

// ConfirmAccountDeletion consumes the link and schedules the purge at
// after: the account is disabled, its web sessions and login links end,
// and its open connections close. API tokens stay, refused while the
// account is disabled, so a cancelled deletion leaves them working.
func (s *Store) ConfirmAccountDeletion(tokenHash string, after time.Time) (User, error) {
	u, err := s.AccountDeletionUser(tokenHash)
	if err != nil {
		return User{}, err
	}
	// A suspension is an admin's decision; the link cannot turn it into
	// a schedule its owner could then cancel by signing in.
	if u.Disabled {
		return User{}, ErrNotFound
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return User{}, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec("DELETE FROM account_deletions WHERE user_id = ?", u.ID); err != nil {
		return User{}, err
	}
	if _, err := tx.Exec("UPDATE users SET disabled = 1, delete_after = ? WHERE id = ? AND disabled = 0", fmtTime(after), u.ID); err != nil {
		return User{}, err
	}
	for _, table := range []string{"web_sessions", "login_tokens"} {
		if _, err := tx.Exec("DELETE FROM "+table+" WHERE user_id = ?", u.ID); err != nil {
			return User{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return User{}, err
	}
	s.announce(Revoked{UserID: u.ID})
	u.Disabled, u.DeleteAfter = true, fmtTime(after)
	return u, nil
}

// Purging marks users.delete_after while the purge runs. It sorts after
// every timestamp, so nothing cancels it, and DueDeletions returns it
// again until the purge completes.
const Purging = "purging"

// ClaimDeletion marks a due account as being purged. It reports false
// when a cancel or an admin got there first.
func (s *Store) ClaimDeletion(userID int64, now time.Time) (bool, error) {
	res, err := s.DB.Exec(`UPDATE users SET delete_after = ? WHERE id = ? AND disabled = 1
		AND delete_after IS NOT NULL AND (delete_after <= ? OR delete_after = ?)`, Purging, userID, fmtTime(now), Purging)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// CancelAccountDeletion drops a waiting request and a scheduled purge.
// It reports whether either existed.
func (s *Store) CancelAccountDeletion(userID int64) (bool, error) {
	res, err := s.DB.Exec("DELETE FROM account_deletions WHERE user_id = ?", userID)
	if err != nil {
		return false, err
	}
	requested, _ := res.RowsAffected()
	res, err = s.DB.Exec("UPDATE users SET disabled = 0, delete_after = NULL WHERE id = ? AND delete_after IS NOT NULL AND delete_after != ?", userID, Purging)
	if err != nil {
		return false, err
	}
	scheduled, _ := res.RowsAffected()
	return requested+scheduled > 0, nil
}

// DueDeletions lists the accounts whose scheduled purge time has passed.
func (s *Store) DueDeletions(now time.Time) ([]User, error) {
	rows, err := s.DB.Query("SELECT id FROM users WHERE delete_after IS NOT NULL AND (delete_after <= ? OR delete_after = ?)", fmtTime(now), Purging)
	if err != nil {
		return nil, err
	}
	ids, err := scanIDs(rows)
	if err != nil {
		return nil, err
	}
	var out []User
	for _, id := range ids {
		u, err := s.UserByID(id)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, nil
}

// SoleAdminOrgs names the organizations where the user is the only admin.
func (s *Store) SoleAdminOrgs(userID int64) ([]string, error) {
	rows, err := s.DB.Query(`SELECT o.name FROM orgs o JOIN org_members m ON m.org_id = o.id
		WHERE m.user_id = ? AND m.role = 'admin'
		AND NOT EXISTS (SELECT 1 FROM org_members x
			WHERE x.org_id = o.id AND x.role = 'admin' AND x.user_id != m.user_id
			AND x.user_id NOT IN (SELECT id FROM users WHERE ghost = 1))
		ORDER BY o.name`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		names = append(names, n)
	}
	return names, rows.Err()
}

// OtherActiveAdmins counts instance admins other than the user who can
// still act.
func (s *Store) OtherActiveAdmins(userID int64) (int64, error) {
	var n int64
	err := s.DB.QueryRow(`SELECT COUNT(*) FROM users WHERE is_admin = 1 AND disabled = 0
		AND pending = 0 AND id != ?`, userID).Scan(&n)
	return n, err
}

// EnsureGhost returns the ghost account's id, creating it on first use.
// It is disabled and holds no credentials, so nothing can act as it.
func (s *Store) EnsureGhost() (int64, error) {
	var id int64
	err := s.DB.QueryRow("SELECT id FROM users WHERE ghost = 1").Scan(&id)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	if _, err := s.UserByUsername("ghost"); err == nil {
		return 0, ErrGhostNameTaken
	}
	res, err := s.DB.Exec(`INSERT INTO users (username, is_admin, disabled, ghost, description)
		VALUES ('ghost', 0, 1, 1, 'This account stands in for deleted users.')`)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// ReassignToGhost moves what the user wrote on other owners' repositories
// to the ghost: issues, merge requests, comments, diff comments and
// reviews, the rows DeleteUser otherwise refuses over. Pending draft
// comments are deleted and reviews made stale.
func (s *Store) ReassignToGhost(userID, ghostID int64) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Unsubmitted drafts go; reviews stay as text but no longer count
	// toward a merge gate.
	if _, err := tx.Exec("DELETE FROM mr_diff_comments WHERE author_id = ? AND pending = 1", userID); err != nil {
		return err
	}
	if _, err := tx.Exec("UPDATE mr_reviews SET stale = 1 WHERE reviewer_id = ?", userID); err != nil {
		return err
	}
	for _, col := range []struct{ table, column string }{
		{"issues", "author_id"},
		{"merge_requests", "author_id"},
		{"issue_comments", "author_id"},
		{"mr_comments", "author_id"},
		{"mr_diff_comments", "author_id"},
		{"mr_reviews", "reviewer_id"},
	} {
		if _, err := tx.Exec(fmt.Sprintf("UPDATE %s SET %s = ? WHERE %s = ?", col.table, col.column, col.column), ghostID, userID); err != nil {
			return fmt.Errorf("reassigning %s: %w", col.table, err)
		}
	}
	return tx.Commit()
}
