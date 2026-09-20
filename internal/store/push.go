package store

import (
	"database/sql"
	"errors"
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
// hands the same one to whichever account signs in next. The id is read
// back by token rather than taken from LastInsertId, which SQLite leaves
// unchanged when the DO UPDATE arm fires instead of the INSERT.
func (s *Store) AddPushDevice(userID int64, token, label string) (int64, error) {
	_, err := s.DB.Exec(`
		INSERT INTO push_devices (user_id, token, label) VALUES (?, ?, ?)
		ON CONFLICT(token) DO UPDATE SET user_id = excluded.user_id, label = excluded.label`,
		userID, token, label)
	if err != nil {
		return 0, err
	}
	var id int64
	err = s.DB.QueryRow("SELECT id FROM push_devices WHERE token = ?", token).Scan(&id)
	return id, err
}

func (s *Store) PushDevices(userID int64) ([]PushDevice, error) {
	rows, err := s.DB.Query(`
		SELECT id, user_id, token, label, created_at, COALESCE(last_seen_at, '')
		FROM push_devices WHERE user_id = ? ORDER BY id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PushDevice
	for rows.Next() {
		var d PushDevice
		if err := rows.Scan(&d.ID, &d.UserID, &d.Token, &d.Label, &d.CreatedAt, &d.LastSeenAt); err != nil {
			return nil, err
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
	Title    string
	Body     string
	Path     string
	Attempts int
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
		SELECT q.id, q.device_id, d.token, q.title, q.body, q.path, q.attempts
		FROM push_queue q JOIN push_devices d ON d.id = q.device_id
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
		if err := rows.Scan(&p.ID, &p.DeviceID, &p.Token, &p.Title, &p.Body, &p.Path, &p.Attempts); err != nil {
			return nil, err
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
// queue rows cascade, so nothing is left retrying at a dead token.
func (s *Store) DeletePushDeviceByToken(token string) error {
	_, err := s.DB.Exec("DELETE FROM push_devices WHERE token = ?", token)
	return err
}
