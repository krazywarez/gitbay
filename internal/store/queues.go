package store

import "fmt"

// Queues is the state of every background worker, for the instance admin:
// what is waiting, what is retrying, and what has given up. Item lists are
// capped so a flood of one kind cannot bury the others.
type Queues struct {
	Webhooks QueueWebhooks `json:"webhooks"`
	Mail     QueueMail     `json:"mail"`
	Mirrors  QueueMirrors  `json:"mirrors"`
	Builds   QueueBuilds   `json:"builds"`
	Deps     QueueDeps     `json:"deps"`
}

type QueueWebhooks struct {
	Pending       int64              `json:"pending"`
	Retrying      int64              `json:"retrying"` // pending with at least one failed attempt
	Failed        int64              `json:"failed"`   // dead-lettered
	OldestPending string             `json:"oldest_pending,omitempty"`
	Items         []QueueDeliveryRow `json:"items"` // retrying and dead-lettered, newest first
}

type QueueDeliveryRow struct {
	ID        int64  `json:"id"`
	Repo      string `json:"repo"`
	URL       string `json:"url"`
	Attempts  int64  `json:"attempts"`
	Status    int64  `json:"last_status,omitempty"`
	LastError string `json:"last_error,omitempty"`
	FailedAt  string `json:"failed_at,omitempty"`
	CreatedAt string `json:"created_at"`
}

type QueueMail struct {
	Pending       int64          `json:"pending"`
	Retrying      int64          `json:"retrying"`
	Failed        int64          `json:"failed"`
	OldestPending string         `json:"oldest_pending,omitempty"`
	Items         []QueueMailRow `json:"items"`
}

type QueueMailRow struct {
	ID        int64  `json:"id"`
	Recipient string `json:"recipient"`
	Subject   string `json:"subject"`
	Attempts  int64  `json:"attempts"`
	LastError string `json:"last_error,omitempty"`
	FailedAt  string `json:"failed_at,omitempty"`
	CreatedAt string `json:"created_at"`
}

type QueueMirrors struct {
	Dirty  int64            `json:"dirty"` // waiting for a sync
	Errors int64            `json:"errors"`
	Items  []QueueMirrorRow `json:"items"` // the ones whose last sync failed
}

type QueueMirrorRow struct {
	ID        int64  `json:"id"`
	Repo      string `json:"repo"`
	Direction string `json:"direction"`
	URL       string `json:"url"`
	LastSync  string `json:"last_sync,omitempty"`
	LastError string `json:"last_error"`
}

type QueueBuilds struct {
	Pending       int64           `json:"pending"`
	Running       int64           `json:"running"`
	OldestPending string          `json:"oldest_pending,omitempty"`
	Items         []QueueBuildRow `json:"items"` // running builds, oldest first
}

type QueueBuildRow struct {
	Repo      string `json:"repo"`
	Number    int64  `json:"number"`
	Job       string `json:"job"`
	StartedAt string `json:"started_at"`
}

type QueueDeps struct {
	Errors int64         `json:"errors"`
	Items  []QueueDepRow `json:"items"`
}

type QueueDepRow struct {
	Repo      string `json:"repo"`
	LastCheck string `json:"last_check,omitempty"`
	LastError string `json:"last_error"`
}

const queueItemCap = 20

const repoPathExpr = `COALESCE(u.username, o.name) || '/' || r.name`

// repoJoin joins repos and their owner for the path expression; the
// argument is the column holding the repo id.
func repoJoin(col string) string {
	return fmt.Sprintf(` JOIN repos r ON r.id = %s
		LEFT JOIN users u ON r.owner_kind = 'user' AND u.id = r.owner_id
		LEFT JOIN orgs o ON r.owner_kind = 'org' AND o.id = r.owner_id`, col)
}

// QueueStatus reads every worker queue. Read-only; safe on a live daemon.
func (s *Store) QueueStatus() (Queues, error) {
	q := Queues{
		Webhooks: QueueWebhooks{Items: []QueueDeliveryRow{}},
		Mail:     QueueMail{Items: []QueueMailRow{}},
		Mirrors:  QueueMirrors{Items: []QueueMirrorRow{}},
		Builds:   QueueBuilds{Items: []QueueBuildRow{}},
		Deps:     QueueDeps{Items: []QueueDepRow{}},
	}

	if err := s.DB.QueryRow(`SELECT
		COUNT(*) FILTER (WHERE delivered_at IS NULL AND failed_at IS NULL),
		COUNT(*) FILTER (WHERE delivered_at IS NULL AND failed_at IS NULL AND attempts > 0),
		COUNT(*) FILTER (WHERE failed_at IS NOT NULL),
		COALESCE(MIN(created_at) FILTER (WHERE delivered_at IS NULL AND failed_at IS NULL), '')
		FROM webhook_deliveries`).Scan(&q.Webhooks.Pending, &q.Webhooks.Retrying, &q.Webhooks.Failed, &q.Webhooks.OldestPending); err != nil {
		return q, err
	}
	if err := s.queryEach(`SELECT d.id, `+repoPathExpr+`, w.url, d.attempts, COALESCE(d.last_status, 0),
		COALESCE(d.last_error, ''), COALESCE(d.failed_at, ''), d.created_at
		FROM webhook_deliveries d JOIN webhooks w ON w.id = d.webhook_id`+repoJoin("w.repo_id")+`
		WHERE d.delivered_at IS NULL AND (d.failed_at IS NOT NULL OR d.attempts > 0)
		ORDER BY d.id DESC LIMIT ?`, func(sc scanner) error {
		var d QueueDeliveryRow
		if err := sc.Scan(&d.ID, &d.Repo, &d.URL, &d.Attempts, &d.Status, &d.LastError, &d.FailedAt, &d.CreatedAt); err != nil {
			return err
		}
		q.Webhooks.Items = append(q.Webhooks.Items, d)
		return nil
	}); err != nil {
		return q, err
	}

	if err := s.DB.QueryRow(`SELECT
		COUNT(*) FILTER (WHERE sent_at IS NULL AND failed_at IS NULL),
		COUNT(*) FILTER (WHERE sent_at IS NULL AND failed_at IS NULL AND attempts > 0),
		COUNT(*) FILTER (WHERE failed_at IS NOT NULL),
		COALESCE(MIN(created_at) FILTER (WHERE sent_at IS NULL AND failed_at IS NULL), '')
		FROM notifications`).Scan(&q.Mail.Pending, &q.Mail.Retrying, &q.Mail.Failed, &q.Mail.OldestPending); err != nil {
		return q, err
	}
	if err := s.queryEach(`SELECT id, recipient, subject, attempts, COALESCE(last_error, ''), COALESCE(failed_at, ''), created_at
		FROM notifications WHERE sent_at IS NULL AND (failed_at IS NOT NULL OR attempts > 0)
		ORDER BY id DESC LIMIT ?`, func(sc scanner) error {
		var m QueueMailRow
		if err := sc.Scan(&m.ID, &m.Recipient, &m.Subject, &m.Attempts, &m.LastError, &m.FailedAt, &m.CreatedAt); err != nil {
			return err
		}
		q.Mail.Items = append(q.Mail.Items, m)
		return nil
	}); err != nil {
		return q, err
	}

	if err := s.DB.QueryRow(`SELECT COUNT(*) FILTER (WHERE dirty = 1), COUNT(*) FILTER (WHERE last_error != '')
		FROM mirrors`).Scan(&q.Mirrors.Dirty, &q.Mirrors.Errors); err != nil {
		return q, err
	}
	if err := s.queryEach(`SELECT m.id, `+repoPathExpr+`, m.direction, m.url, m.last_sync, m.last_error
		FROM mirrors m`+repoJoin("m.repo_id")+` WHERE m.last_error != '' ORDER BY m.id DESC LIMIT ?`, func(sc scanner) error {
		var m QueueMirrorRow
		if err := sc.Scan(&m.ID, &m.Repo, &m.Direction, &m.URL, &m.LastSync, &m.LastError); err != nil {
			return err
		}
		q.Mirrors.Items = append(q.Mirrors.Items, m)
		return nil
	}); err != nil {
		return q, err
	}

	if err := s.DB.QueryRow(`SELECT COUNT(*) FILTER (WHERE status = 'pending'), COUNT(*) FILTER (WHERE status = 'running'),
		COALESCE(MIN(created_at) FILTER (WHERE status = 'pending'), '') FROM builds`).Scan(&q.Builds.Pending, &q.Builds.Running, &q.Builds.OldestPending); err != nil {
		return q, err
	}
	if err := s.queryEach(`SELECT `+repoPathExpr+`, b.number, b.job, b.started_at
		FROM builds b`+repoJoin("b.repo_id")+` WHERE b.status = 'running' ORDER BY b.started_at LIMIT ?`, func(sc scanner) error {
		var b QueueBuildRow
		if err := sc.Scan(&b.Repo, &b.Number, &b.Job, &b.StartedAt); err != nil {
			return err
		}
		q.Builds.Items = append(q.Builds.Items, b)
		return nil
	}); err != nil {
		return q, err
	}

	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM dep_checks WHERE last_error != ''`).Scan(&q.Deps.Errors); err != nil {
		return q, err
	}
	if err := s.queryEach(`SELECT `+repoPathExpr+`, c.last_check, c.last_error
		FROM dep_checks c`+repoJoin("c.repo_id")+` WHERE c.last_error != '' ORDER BY c.repo_id LIMIT ?`, func(sc scanner) error {
		var d QueueDepRow
		if err := sc.Scan(&d.Repo, &d.LastCheck, &d.LastError); err != nil {
			return err
		}
		q.Deps.Items = append(q.Deps.Items, d)
		return nil
	}); err != nil {
		return q, err
	}
	return q, nil
}

type scanner interface{ Scan(...any) error }

// queryEach runs a capped item query and hands each row to fn.
func (s *Store) queryEach(query string, fn func(scanner) error) error {
	rows, err := s.DB.Query(query, queueItemCap)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		if err := fn(rows); err != nil {
			return err
		}
	}
	return rows.Err()
}
