package store

import (
	"database/sql"
	"encoding/json"
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

// ErrLastAdmin refuses the demotion that would leave the instance with no
// admin at all.
var ErrLastAdmin = errors.New("that is the only instance admin; promote someone else first")

// SetUserAdmin grants or removes instance admin. Removing it from the last
// admin is refused inside the same transaction that counts them.
func (s *Store) SetUserAdmin(userID int64, admin bool) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if !admin {
		var others int
		if err := tx.QueryRow("SELECT COUNT(*) FROM users WHERE is_admin = 1 AND id != ?", userID).Scan(&others); err != nil {
			return err
		}
		if others == 0 {
			return ErrLastAdmin
		}
	}
	res, err := tx.Exec("UPDATE users SET is_admin = ? WHERE id = ?", boolInt(admin), userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return tx.Commit()
}

// AdminRepo is one repository as the instance admin lists it. LastPush is
// the newest push event, "" when nothing has been pushed.
type AdminRepo struct {
	Path       string // owner/name, the keyset cursor
	OwnerName  string
	Name       string
	Visibility string
	Archived   bool
	CreatedAt  string
	LastPush   string
}

// ListReposAdmin lists repositories across every owner, by path. owner and
// visibility narrow the set when non-empty; after is the path keyset
// cursor; limit 0 means no cap.
func (s *Store) ListReposAdmin(owner, visibility string, limit int, after string) ([]AdminRepo, error) {
	q := `SELECT COALESCE(u.username, o.name) || '/' || r.name, COALESCE(u.username, o.name), r.name,
		r.visibility, r.settings_json, r.created_at,
		COALESCE((SELECT MAX(created_at) FROM events WHERE repo_id = r.id AND kind = 'push'), '')
		FROM repos r
		LEFT JOIN users u ON r.owner_kind = 'user' AND u.id = r.owner_id
		LEFT JOIN orgs o  ON r.owner_kind = 'org'  AND o.id = r.owner_id
		WHERE COALESCE(u.username, o.name) || '/' || r.name > ?`
	args := []any{after}
	if owner != "" {
		q += " AND COALESCE(u.username, o.name) = ?"
		args = append(args, owner)
	}
	if visibility != "" {
		q += " AND r.visibility = ?"
		args = append(args, visibility)
	}
	q += " ORDER BY 1"
	if limit > 0 {
		q += " LIMIT ?"
		args = append(args, limit)
	}
	rows, err := s.DB.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AdminRepo
	for rows.Next() {
		var r AdminRepo
		var settingsJSON string
		if err := rows.Scan(&r.Path, &r.OwnerName, &r.Name, &r.Visibility, &settingsJSON, &r.CreatedAt, &r.LastPush); err != nil {
			return nil, err
		}
		var st RepoSettings
		if err := json.Unmarshal([]byte(settingsJSON), &st); err != nil {
			return nil, fmt.Errorf("repo %s settings: %w", r.Path, err)
		}
		r.Archived = st.Archived
		out = append(out, r)
	}
	return out, rows.Err()
}
