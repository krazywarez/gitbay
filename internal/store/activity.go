package store

import (
	"database/sql"
	"errors"
)

// UserIDByVerifiedEmail resolves a commit author email to an account, only
// through addresses the account has verified — the same trust rule as
// signature attribution.
func (s *Store) UserIDByVerifiedEmail(address string) (int64, bool) {
	var id int64
	err := s.DB.QueryRow(
		"SELECT user_id FROM emails WHERE address = ? AND verified_at IS NOT NULL", address).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) || err != nil {
		return 0, false
	}
	return id, true
}

// RecordCommitActivity is idempotent per (repo, sha); it reports whether
// this call recorded a new row.
func (s *Store) RecordCommitActivity(repoID int64, sha string, userID int64, day string) bool {
	res, err := s.DB.Exec(
		"INSERT INTO commit_activity (repo_id, sha, user_id, day) VALUES (?, ?, ?, ?) ON CONFLICT DO NOTHING",
		repoID, sha, userID, day)
	if err != nil {
		return false
	}
	n, _ := res.RowsAffected()
	return n > 0
}

// ActivityByDay aggregates a user's activity per day since the given day:
// commits landed on default branches plus everything the events table
// attributes to them (issues, MRs, comments, releases, pushes).
func (s *Store) ActivityByDay(userID int64, sinceDay string) (map[string]int, error) {
	rows, err := s.DB.Query(`
		SELECT day, COUNT(*) FROM (
			SELECT day FROM commit_activity WHERE user_id = ?1 AND day >= ?2
			UNION ALL
			SELECT date(created_at) AS day FROM events
			WHERE actor_id = ?1 AND date(created_at) >= ?2 AND kind <> 'push'
		) GROUP BY day`, userID, sinceDay)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var day string
		var n int
		if err := rows.Scan(&day, &n); err != nil {
			return nil, err
		}
		out[day] = n
	}
	return out, rows.Err()
}

// OrgActivityByDay aggregates activity across an org's repositories.
func (s *Store) OrgActivityByDay(orgID int64, sinceDay string) (map[string]int, error) {
	rows, err := s.DB.Query(`
		SELECT day, COUNT(*) FROM (
			SELECT ca.day FROM commit_activity ca
			JOIN repos r ON r.id = ca.repo_id
			WHERE r.owner_kind = 'org' AND r.owner_id = ?1 AND ca.day >= ?2
			UNION ALL
			SELECT date(e.created_at) AS day FROM events e
			JOIN repos r ON r.id = e.repo_id
			WHERE r.owner_kind = 'org' AND r.owner_id = ?1
			  AND date(e.created_at) >= ?2 AND e.kind <> 'push'
		) GROUP BY day`, orgID, sinceDay)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var day string
		var n int
		if err := rows.Scan(&day, &n); err != nil {
			return nil, err
		}
		out[day] = n
	}
	return out, rows.Err()
}
