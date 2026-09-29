package store

import (
	"database/sql"
	"errors"
)

// MRByID loads a merge request by its row id, without labels or review
// requests.
func (s *Store) MRByID(id int64) (MR, error) {
	m, err := scanMR(s.DB.QueryRow(mrSelect+" WHERE m.id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return m, ErrNotFound
	}
	return m, err
}

// QueueMerge queues a merge request to merge as userID with strategy
// ("" for the default) once its gates pass, bound to the SSH key or API
// token it was queued with (both 0 for a web session). Queueing again
// replaces the queuer, strategy and credential and clears the reason.
func (s *Store) QueueMerge(mrID, userID int64, strategy string, keyID, tokenID int64) error {
	credential := ""
	switch {
	case keyID != 0:
		credential = "key"
	case tokenID != 0:
		credential = "token"
	}
	_, err := s.DB.Exec(`
		INSERT INTO mr_merge_queue (mr_id, user_id, strategy, credential, key_id, token_id)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (mr_id) DO UPDATE SET user_id = excluded.user_id,
			strategy = excluded.strategy, credential = excluded.credential,
			key_id = excluded.key_id, token_id = excluded.token_id, reason = '',
			queued_at = strftime('%Y-%m-%dT%H:%M:%fZ','now')`,
		mrID, userID, strategy, credential, nullID(keyID), nullID(tokenID))
	return err
}

// QueueCredential is the credential a queued merge was queued with. Kind
// is "key", "token", or "" for a web session; an id of 0 under "key" or
// "token" means that credential has since been removed.
type QueueCredential struct {
	Kind    string
	KeyID   int64
	TokenID int64
}

func (s *Store) MergeQueueCredential(mrID int64) (QueueCredential, error) {
	var q QueueCredential
	err := s.DB.QueryRow(
		"SELECT credential, COALESCE(key_id, 0), COALESCE(token_id, 0) FROM mr_merge_queue WHERE mr_id = ?",
		mrID).Scan(&q.Kind, &q.KeyID, &q.TokenID)
	if errors.Is(err, sql.ErrNoRows) {
		return q, ErrNotFound
	}
	return q, err
}

// DequeueMerge takes a merge request off the queue, reporting whether it
// was on it.
func (s *Store) DequeueMerge(mrID int64) (bool, error) {
	res, err := s.DB.Exec("DELETE FROM mr_merge_queue WHERE mr_id = ?", mrID)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// SetMergeQueueReason records why the last attempt at a queued merge did
// not merge.
func (s *Store) SetMergeQueueReason(mrID int64, reason string) error {
	_, err := s.DB.Exec("UPDATE mr_merge_queue SET reason = ? WHERE mr_id = ?", reason, mrID)
	return err
}

// QueuedMRsAtHead is the queued, open merge requests of a repository
// whose head is sha: the ones a status reported on sha can move.
func (s *Store) QueuedMRsAtHead(repoID int64, sha string) ([]int64, error) {
	rows, err := s.DB.Query(`
		SELECT m.id FROM mr_merge_queue q JOIN merge_requests m ON m.id = q.mr_id
		WHERE m.repo_id = ? AND m.head_sha = ? AND m.state = 'open'
		ORDER BY m.number`, repoID, sha)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
