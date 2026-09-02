package store

import (
	"database/sql"
	"errors"
)

// Stacked merge requests: B is stacked on A when B's target branch is A's
// source branch, both open, in the same repository. Nothing is stored;
// these two reads derive it.

// OpenMRsByTarget lists the open merge requests in repoID targeting
// targetRef, oldest first.
func (s *Store) OpenMRsByTarget(repoID int64, targetRef string) ([]MR, error) {
	rows, err := s.DB.Query(
		mrSelect+" WHERE m.repo_id = ? AND m.target_ref = ? AND m.state = 'open' ORDER BY m.number",
		repoID, targetRef)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MR
	for rows.Next() {
		m, err := scanMR(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// OpenMRBySource finds the open merge request in repoID whose source is
// the repository's own branch sourceRef. ok is false when there is none.
func (s *Store) OpenMRBySource(repoID int64, sourceRef string) (MR, bool, error) {
	m, err := scanMR(s.DB.QueryRow(
		mrSelect+" WHERE m.repo_id = ? AND m.source_repo_id = ? AND m.source_ref = ? AND m.state = 'open' ORDER BY m.number LIMIT 1",
		repoID, repoID, sourceRef))
	if errors.Is(err, sql.ErrNoRows) {
		return MR{}, false, nil
	}
	return m, err == nil, err
}

// RetargetKeepingReviews moves a merge request onto a new target without
// staling its reviews: used when the branch it was stacked on has merged,
// so the diff against the new target is the diff the reviews were of.
func (s *Store) RetargetKeepingReviews(mrID int64, targetRef string) error {
	res, err := s.DB.Exec(
		"UPDATE merge_requests SET target_ref = ?, updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id = ?",
		targetRef, mrID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
