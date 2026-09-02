package store

import (
	"database/sql"
	"time"
)

// UserLimits is an account's quota overrides; nil means the configured
// default applies.
type UserLimits struct {
	Repos *int64
	Bytes *int64
}

func (s *Store) UserLimits(userID int64) (UserLimits, error) {
	var repos, bytes sql.NullInt64
	err := s.DB.QueryRow("SELECT repo_limit, byte_limit FROM users WHERE id = ?", userID).Scan(&repos, &bytes)
	if err != nil {
		return UserLimits{}, err
	}
	var l UserLimits
	if repos.Valid {
		l.Repos = &repos.Int64
	}
	if bytes.Valid {
		l.Bytes = &bytes.Int64
	}
	return l, nil
}

// SetUserLimits writes the overrides; a nil field clears back to default.
func (s *Store) SetUserLimits(userID int64, l UserLimits) error {
	var repos, bytes any
	if l.Repos != nil {
		repos = *l.Repos
	}
	if l.Bytes != nil {
		bytes = *l.Bytes
	}
	res, err := s.DB.Exec("UPDATE users SET repo_limit = ?, byte_limit = ? WHERE id = ?", repos, bytes, userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ReapPendingUsers deletes self-registered accounts still unverified
// after maxAge. A pending account owns nothing (it cannot create a
// repository before verifying), so DeleteUser has nothing to refuse; an
// account that somehow anchors content is left alone and reported.
func (s *Store) ReapPendingUsers(maxAge time.Duration) ([]string, error) {
	cutoff := fmtTime(time.Now().Add(-maxAge))
	rows, err := s.DB.Query("SELECT id, username FROM users WHERE pending = 1 AND created_at < ?", cutoff)
	if err != nil {
		return nil, err
	}
	type row struct {
		id   int64
		name string
	}
	var stale []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.name); err != nil {
			rows.Close()
			return nil, err
		}
		stale = append(stale, r)
	}
	rows.Close()
	var removed []string
	for _, r := range stale {
		if err := s.DeleteUser(r.id); err == nil {
			removed = append(removed, r.name)
			s.Audit(0, "pending.expired", map[string]any{"user": r.name})
		}
	}
	return removed, nil
}
