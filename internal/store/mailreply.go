package store

import (
	"database/sql"
	"errors"
	"strings"
	"time"
)

// CreatedAt is when the account (table "users") or repository ("repos")
// with id was created.
func (s *Store) CreatedAt(table string, id int64) (time.Time, error) {
	if table != "users" && table != "repos" {
		return time.Time{}, errors.New("CreatedAt: unknown table " + table)
	}
	var v string
	err := s.DB.QueryRow("SELECT created_at FROM "+table+" WHERE id = ?", id).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, ErrNotFound
	}
	if err != nil {
		return time.Time{}, err
	}
	return time.Parse("2006-01-02T15:04:05.000Z", v)
}

// ClaimMailReply records that the reply identified by key is being
// posted. False means an earlier fetch of the same message already
// claimed it.
func (s *Store) ClaimMailReply(key string) (bool, error) {
	res, err := s.DB.Exec("INSERT INTO mail_replies (message_key) VALUES (?) ON CONFLICT DO NOTHING", key)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// ReleaseMailReply drops a claim whose comment was not posted, so the
// message can be tried again.
func (s *Store) ReleaseMailReply(key string) error {
	_, err := s.DB.Exec("DELETE FROM mail_replies WHERE message_key = ?", key)
	return err
}

// PruneMailReplies drops claims older than before. A reply older than a
// reply token's lifetime is refused on its token, so its claim has no
// work left to do.
func (s *Store) PruneMailReplies(before time.Time) error {
	_, err := s.DB.Exec("DELETE FROM mail_replies WHERE created_at < ?", fmtTime(before))
	return err
}

// VerifiedEmailOf reports whether address is a verified address of the
// account, ignoring case.
func (s *Store) VerifiedEmailOf(userID int64, address string) (bool, error) {
	var n int
	err := s.DB.QueryRow(
		"SELECT COUNT(*) FROM emails WHERE user_id = ? AND verified_at IS NOT NULL AND lower(address) = ?",
		userID, strings.ToLower(address)).Scan(&n)
	return n > 0, err
}
