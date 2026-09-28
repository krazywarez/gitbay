package store

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// PushDevice is one Apple device an account has registered. Token is the
// APNs device token: an address, not a credential, but device-identifying
// and never logged or echoed in full.
type PushDevice struct {
	ID         int64
	UserID     int64
	Token      string
	Label      string
	CreatedAt  string
	LastSeenAt string
}

// AddPushDevice registers a token to an account. A token already present
// changes hands rather than erroring: Apple reuses tokens, and a reinstall
// hands the same one to whichever account signs in next. The token is
// sealed (secrets.go), so the lookup and the upsert go by its hash, and a
// handover reseals it under the new owner. The id is read back by hash
// rather than taken from LastInsertId, which SQLite leaves unchanged when
// the DO UPDATE arm fires instead of the INSERT.
//
// The row id survives that handover, so queue rows written for the
// previous owner would still be delivered to the device — and an alert
// carries the repository name and item number in full. Undelivered rows
// go with the ownership, in the same transaction; sent and dead-lettered
// rows are history and stay.
func (s *Store) AddPushDevice(userID int64, token, label string) (int64, error) {
	tx, err := s.DB.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	h := tokenHash(token)
	// A row written before token_hash existed holds its token in clear.
	if _, err := tx.Exec("UPDATE push_devices SET token_hash = ? WHERE token_hash IS NULL AND token = ?", h, token); err != nil {
		return 0, err
	}
	var prev int64
	if err := tx.QueryRow("SELECT user_id FROM push_devices WHERE token_hash = ?", h).Scan(&prev); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	sealed, err := s.sealValue(pushTokenAAD(userID, h), token)
	if err != nil {
		return 0, err
	}
	if _, err := tx.Exec(`
		INSERT INTO push_devices (user_id, token, token_hash, label) VALUES (?, ?, ?, ?)
		ON CONFLICT(token_hash) DO UPDATE SET user_id = excluded.user_id, token = excluded.token, label = excluded.label`,
		userID, sealed, h, label); err != nil {
		return 0, err
	}
	var id int64
	if err := tx.QueryRow("SELECT id FROM push_devices WHERE token_hash = ?", h).Scan(&id); err != nil {
		return 0, err
	}
	if prev != 0 && prev != userID {
		if _, err := tx.Exec(
			"DELETE FROM push_queue WHERE device_id = ? AND sent_at IS NULL AND failed_at IS NULL", id); err != nil {
			return 0, err
		}
	}
	return id, tx.Commit()
}

func (s *Store) PushDevices(userID int64) ([]PushDevice, error) {
	rows, err := s.DB.Query(`
		SELECT id, user_id, token, COALESCE(token_hash, ''), label, created_at, COALESCE(last_seen_at, '')
		FROM push_devices WHERE user_id = ? ORDER BY id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PushDevice
	for rows.Next() {
		var d PushDevice
		var h string
		if err := rows.Scan(&d.ID, &d.UserID, &d.Token, &h, &d.Label, &d.CreatedAt, &d.LastSeenAt); err != nil {
			return nil, err
		}
		if d.Token, err = s.openValue(pushTokenAAD(d.UserID, h), d.Token); err != nil {
			return nil, fmt.Errorf("push device %d: %w", d.ID, err)
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// RemovePushDevice deletes one of the account's own devices. Scoping the
// delete by user_id rather than checking ownership first means another
// account's id is ErrNotFound, which is the same answer as an id that
// never existed — a caller learns nothing about other accounts' devices.
func (s *Store) RemovePushDevice(userID, id int64) error {
	res, err := s.DB.Exec("DELETE FROM push_devices WHERE id = ? AND user_id = ?", id, userID)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) PushEnabled(userID int64) (bool, error) {
	var on int
	err := s.DB.QueryRow("SELECT notify_push FROM users WHERE id = ?", userID).Scan(&on)
	if errors.Is(err, sql.ErrNoRows) {
		return false, ErrNotFound
	}
	return on != 0, err
}

func (s *Store) SetPushEnabled(userID int64, on bool) error {
	v := 0
	if on {
		v = 1
	}
	_, err := s.DB.Exec("UPDATE users SET notify_push = ? WHERE id = ?", v, userID)
	return err
}

// QueuedPush is one pending push, joined to the token it is bound for so
// the drainer needs one query rather than two.
type QueuedPush struct {
	ID       int64
	DeviceID int64
	Token    string
	// Username is the recipient. One device token is one install, and an
	// install registers against every account signed in on it, so the
	// alert has to name which of them it is for.
	Username string
	Title    string
	Body     string
	Path     string
	Attempts int
	// Badge is the recipient's unread inbox count, for the alert's badge.
	// Counted here rather than at enqueue so a cleared inbox is reflected.
	Badge int
}

// EnqueuePush writes one row per registered device, and nothing when the
// account has push off or no devices — the same shape as
// ActivityMailAddress returning "" when notify_mail is off. Mute, watch
// and actor-exclusion are already settled by NotifyRecipients before a
// caller reaches here.
func (s *Store) EnqueuePush(userID int64, title, body, path string) error {
	on, err := s.PushEnabled(userID)
	if err != nil || !on {
		return err
	}
	_, err = s.DB.Exec(`
		INSERT INTO push_queue (device_id, title, body, path)
		SELECT id, ?, ?, ? FROM push_devices WHERE user_id = ?`,
		title, body, path, userID)
	return err
}

func (s *Store) DuePush(limit int) ([]QueuedPush, error) {
	rows, err := s.DB.Query(`
		SELECT q.id, q.device_id, d.token, d.user_id, COALESCE(d.token_hash, ''), u.username, q.title, q.body, q.path, q.attempts,
		       (SELECT COUNT(*) FROM inbox WHERE user_id = d.user_id AND read_at IS NULL)
		FROM push_queue q
		JOIN push_devices d ON d.id = q.device_id
		JOIN users u ON u.id = d.user_id
		WHERE q.sent_at IS NULL AND q.failed_at IS NULL
		  AND (q.next_attempt_at IS NULL OR q.next_attempt_at <= ?)
		ORDER BY q.id LIMIT ?`, fmtTime(time.Now()), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []QueuedPush
	for rows.Next() {
		var p QueuedPush
		var uid int64
		var h string
		if err := rows.Scan(&p.ID, &p.DeviceID, &p.Token, &uid, &h, &p.Username, &p.Title, &p.Body, &p.Path, &p.Attempts, &p.Badge); err != nil {
			return nil, err
		}
		if p.Token, err = s.openValue(pushTokenAAD(uid, h), p.Token); err != nil {
			return nil, fmt.Errorf("push device %d: %w", p.DeviceID, err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) MarkPushSent(id int64) error {
	_, err := s.DB.Exec(
		"UPDATE push_queue SET sent_at = strftime('%Y-%m-%dT%H:%M:%fZ','now'), attempts = attempts + 1 WHERE id = ?", id)
	return err
}

func (s *Store) MarkPushFailed(id int64, errMsg string, nextAt *time.Time) error {
	if nextAt == nil {
		_, err := s.DB.Exec(
			"UPDATE push_queue SET failed_at = strftime('%Y-%m-%dT%H:%M:%fZ','now'), attempts = attempts + 1, last_error = ? WHERE id = ?",
			errMsg, id)
		return err
	}
	_, err := s.DB.Exec(
		"UPDATE push_queue SET attempts = attempts + 1, last_error = ?, next_attempt_at = ? WHERE id = ?",
		errMsg, fmtTime(*nextAt), id)
	return err
}

// DeletePushDeviceByToken drops a device Apple has told us is gone. The
// queue rows cascade, so nothing is left retrying at a dead token. A row
// without a hash predates sealing and holds its token in clear.
func (s *Store) DeletePushDeviceByToken(token string) error {
	_, err := s.DB.Exec("DELETE FROM push_devices WHERE token_hash = ? OR (token_hash IS NULL AND token = ?)",
		tokenHash(token), token)
	return err
}
