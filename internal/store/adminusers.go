package store

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// AdminUser is one account as the instance admin sees it. LastSeen is the
// most recent authentication by any of the account's SSH keys or API
// tokens, "" when there has been none.
type AdminUser struct {
	Username  string
	IsAdmin   bool
	Pending   bool
	Disabled  bool
	CreatedAt string
	LastSeen  string
}

const adminUserSelect = `SELECT u.username, u.is_admin, u.pending, u.disabled, u.created_at,
	COALESCE((SELECT MAX(t) FROM (
		SELECT last_used_at t FROM ssh_keys WHERE user_id = u.id
		UNION ALL SELECT last_used_at FROM api_tokens WHERE user_id = u.id)), '')
	FROM users u`

func scanAdminUser(row interface{ Scan(...any) error }) (AdminUser, error) {
	var u AdminUser
	var admin, pending, disabled int
	err := row.Scan(&u.Username, &admin, &pending, &disabled, &u.CreatedAt, &u.LastSeen)
	u.IsAdmin = admin != 0
	u.Pending = pending != 0
	u.Disabled = disabled != 0
	return u, err
}

// ListUsers returns accounts by username. state narrows the set: "" for
// every account, active (neither pending nor disabled), pending, disabled,
// or admin. after is the keyset cursor: usernames strictly greater than
// it, "" from the start. limit 0 means no cap.
func (s *Store) ListUsers(state string, limit int, after string) ([]AdminUser, error) {
	where := "WHERE u.username > ?"
	switch state {
	case "":
	case "active":
		where += " AND u.pending = 0 AND u.disabled = 0"
	case "pending":
		where += " AND u.pending = 1"
	case "disabled":
		where += " AND u.disabled = 1"
	case "admin":
		where += " AND u.is_admin = 1"
	default:
		return nil, fmt.Errorf("unknown state %q", state)
	}
	q := adminUserSelect + " " + where + " ORDER BY u.username"
	args := []any{after}
	if limit > 0 {
		q += " LIMIT ?"
		args = append(args, limit)
	}
	rows, err := s.DB.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AdminUser
	for rows.Next() {
		u, err := scanAdminUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// AdminUserByName is the ListUsers row for one account.
func (s *Store) AdminUserByName(name string) (AdminUser, error) {
	u, err := scanAdminUser(s.DB.QueryRow(adminUserSelect+" WHERE u.username = ?", name))
	if errors.Is(err, sql.ErrNoRows) {
		return u, ErrNotFound
	}
	return u, err
}

// OwnedRepoCount counts repositories the user owns directly, not through
// an org.
func (s *Store) OwnedRepoCount(userID int64) (int64, error) {
	var n int64
	err := s.DB.QueryRow("SELECT COUNT(*) FROM repos WHERE owner_kind = 'user' AND owner_id = ?", userID).Scan(&n)
	return n, err
}

// WebSessionCount counts the user's unexpired browser sessions.
func (s *Store) WebSessionCount(userID int64) (int64, error) {
	var n int64
	err := s.DB.QueryRow("SELECT COUNT(*) FROM web_sessions WHERE user_id = ? AND expires_at > ?",
		userID, fmtTime(time.Now())).Scan(&n)
	return n, err
}
