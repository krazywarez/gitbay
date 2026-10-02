package store

import (
	"database/sql"
	"time"
)

// Limits is an owner's quota overrides; nil means the configured default
// applies. Orgs is the account's cap on organizations it creates and is
// always nil for an org.
type Limits struct {
	Repos *int64
	Bytes *int64
	Orgs  *int64
}

func nullable(n sql.NullInt64) *int64 {
	if !n.Valid {
		return nil
	}
	return &n.Int64
}

func orNull(p *int64) any {
	if p == nil {
		return nil
	}
	return *p
}

// OwnerLimits reads the overrides of a user or an org.
func (s *Store) OwnerLimits(kind string, id int64) (Limits, error) {
	var repos, bytes, orgs sql.NullInt64
	var err error
	if kind == "org" {
		err = s.DB.QueryRow("SELECT repo_limit, byte_limit FROM orgs WHERE id = ?", id).Scan(&repos, &bytes)
	} else {
		err = s.DB.QueryRow("SELECT repo_limit, byte_limit, org_limit FROM users WHERE id = ?", id).Scan(&repos, &bytes, &orgs)
	}
	if err != nil {
		return Limits{}, err
	}
	return Limits{nullable(repos), nullable(bytes), nullable(orgs)}, nil
}

// SetOwnerLimits writes the overrides; a nil field clears back to default.
func (s *Store) SetOwnerLimits(kind string, id int64, l Limits) error {
	var res sql.Result
	var err error
	if kind == "org" {
		res, err = s.DB.Exec("UPDATE orgs SET repo_limit = ?, byte_limit = ? WHERE id = ?", orNull(l.Repos), orNull(l.Bytes), id)
	} else {
		res, err = s.DB.Exec("UPDATE users SET repo_limit = ?, byte_limit = ?, org_limit = ? WHERE id = ?",
			orNull(l.Repos), orNull(l.Bytes), orNull(l.Orgs), id)
	}
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// CreatedOrgCount counts the organizations an account created and that
// still exist.
func (s *Store) CreatedOrgCount(userID int64) (int64, error) {
	var n int64
	err := s.DB.QueryRow("SELECT COUNT(*) FROM orgs WHERE created_by = ?", userID).Scan(&n)
	return n, err
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
