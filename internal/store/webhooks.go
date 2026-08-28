package store

import (
	"time"
)

type Webhook struct {
	ID        int64
	URL       string
	Secret    string
	Events    string // "*" or comma-separated kinds
	Active    bool
	CreatedAt string
}

type Delivery struct {
	ID        int64
	WebhookID int64
	URL       string
	Secret    string
	EventID   int64
	EventKind string
	RepoPath  string
	Actor     string
	DataJSON  string
	EventAt   string
	Attempts  int
}

type DeliveryStatus struct {
	ID         int64
	URL        string
	EventKind  string
	Status     string // pending | delivered | failed
	Attempts   int
	LastStatus int
	LastError  string
	CreatedAt  string
}

func (s *Store) AddWebhook(repoID int64, url, secret, events string) (int64, error) {
	res, err := s.DB.Exec(
		"INSERT INTO webhooks (repo_id, url, secret, events) VALUES (?, ?, ?, ?)",
		repoID, url, secret, events)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) ListWebhooks(repoID int64) ([]Webhook, error) {
	rows, err := s.DB.Query(
		"SELECT id, url, secret, events, active, created_at FROM webhooks WHERE repo_id = ? ORDER BY id", repoID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Webhook
	for rows.Next() {
		var w Webhook
		var active int
		if err := rows.Scan(&w.ID, &w.URL, &w.Secret, &w.Events, &active, &w.CreatedAt); err != nil {
			return nil, err
		}
		w.Active = active != 0
		out = append(out, w)
	}
	return out, rows.Err()
}

func (s *Store) RemoveWebhook(repoID, hookID int64) error {
	res, err := s.DB.Exec("DELETE FROM webhooks WHERE repo_id = ? AND id = ?", repoID, hookID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// DueDeliveries returns pending deliveries whose time has come, with the
// event and hook context needed to send them.
func (s *Store) DueDeliveries(limit int) ([]Delivery, error) {
	rows, err := s.DB.Query(`
		SELECT d.id, d.webhook_id, w.url, w.secret, d.event_id, e.kind,
		       COALESCE(u2.username, o.name, '') || '/' || COALESCE(r.name, ''),
		       COALESCE(u.username, ''), e.data_json, e.created_at, d.attempts
		FROM webhook_deliveries d
		JOIN webhooks w ON w.id = d.webhook_id
		JOIN events e   ON e.id = d.event_id
		LEFT JOIN users u ON u.id = e.actor_id
		LEFT JOIN repos r ON r.id = e.repo_id
		LEFT JOIN users u2 ON r.owner_kind = 'user' AND u2.id = r.owner_id
		LEFT JOIN orgs o   ON r.owner_kind = 'org'  AND o.id = r.owner_id
		WHERE d.delivered_at IS NULL AND d.failed_at IS NULL
		  AND (d.next_attempt_at IS NULL OR d.next_attempt_at <= ?)
		ORDER BY d.id LIMIT ?`, fmtTime(time.Now()), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Delivery
	for rows.Next() {
		var d Delivery
		if err := rows.Scan(&d.ID, &d.WebhookID, &d.URL, &d.Secret, &d.EventID, &d.EventKind,
			&d.RepoPath, &d.Actor, &d.DataJSON, &d.EventAt, &d.Attempts); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *Store) MarkDelivered(id int64, status int) error {
	_, err := s.DB.Exec(`
		UPDATE webhook_deliveries SET delivered_at = strftime('%Y-%m-%dT%H:%M:%fZ','now'),
		attempts = attempts + 1, last_status = ?, last_error = NULL WHERE id = ?`, status, id)
	return err
}

// MarkAttemptFailed records a failed attempt; nextAt nil dead-letters it.
func (s *Store) MarkAttemptFailed(id int64, status int, errMsg string, nextAt *time.Time) error {
	if nextAt == nil {
		_, err := s.DB.Exec(`
			UPDATE webhook_deliveries SET failed_at = strftime('%Y-%m-%dT%H:%M:%fZ','now'),
			attempts = attempts + 1, last_status = ?, last_error = ? WHERE id = ?`, status, errMsg, id)
		return err
	}
	_, err := s.DB.Exec(`
		UPDATE webhook_deliveries SET attempts = attempts + 1, last_status = ?, last_error = ?,
		next_attempt_at = ? WHERE id = ?`, status, errMsg, fmtTime(*nextAt), id)
	return err
}

func (s *Store) ListDeliveries(repoID int64, limit int) ([]DeliveryStatus, error) {
	rows, err := s.DB.Query(`
		SELECT d.id, w.url, e.kind,
		       CASE WHEN d.delivered_at IS NOT NULL THEN 'delivered'
		            WHEN d.failed_at IS NOT NULL THEN 'failed'
		            ELSE 'pending' END,
		       d.attempts, COALESCE(d.last_status, 0), COALESCE(d.last_error, ''), d.created_at
		FROM webhook_deliveries d
		JOIN webhooks w ON w.id = d.webhook_id
		JOIN events e ON e.id = d.event_id
		WHERE w.repo_id = ? ORDER BY d.id DESC LIMIT ?`, repoID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DeliveryStatus
	for rows.Next() {
		var d DeliveryStatus
		if err := rows.Scan(&d.ID, &d.URL, &d.EventKind, &d.Status, &d.Attempts, &d.LastStatus, &d.LastError, &d.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// Redeliver resets a delivery for an immediate retry.
func (s *Store) Redeliver(repoID, deliveryID int64) error {
	res, err := s.DB.Exec(`
		UPDATE webhook_deliveries SET delivered_at = NULL, failed_at = NULL, next_attempt_at = NULL
		WHERE id = ? AND webhook_id IN (SELECT id FROM webhooks WHERE repo_id = ?)`, deliveryID, repoID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
