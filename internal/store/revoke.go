package store

import (
	"slices"
	"strings"
)

// Revoked names SSH keys that stopped being valid: by id, or every key
// of an account. The SSH listener closes the connections they opened.
type Revoked struct {
	KeyIDs []int64
	UserID int64 // every key of this account; 0 for none
}

// OnRevoke registers f to run after each revocation this process
// commits. Revocations committed by another process (gitbayd admin on
// the host) are not announced; the listener's sweep finds those.
func (s *Store) OnRevoke(f func(Revoked)) {
	s.revokeMu.Lock()
	defer s.revokeMu.Unlock()
	s.onRevoke = append(s.onRevoke, f)
}

// announce runs the subscribers. Call it after the commit, outside any
// transaction.
func (s *Store) announce(r Revoked) {
	s.revokeMu.Lock()
	fs := slices.Clone(s.onRevoke)
	s.revokeMu.Unlock()
	for _, f := range fs {
		f(r)
	}
}

// LiveSSHKeys reports which of ids still name a registered key on an
// account that is not disabled.
func (s *Store) LiveSSHKeys(ids []int64) (map[int64]bool, error) {
	live := map[int64]bool{}
	if len(ids) == 0 {
		return live, nil
	}
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	rows, err := s.DB.Query(`SELECT k.id FROM ssh_keys k JOIN users u ON u.id = k.user_id
		WHERE u.disabled = 0 AND k.id IN (?`+strings.Repeat(", ?", len(ids)-1)+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		live[id] = true
	}
	return live, rows.Err()
}
