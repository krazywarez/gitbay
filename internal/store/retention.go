package store

import (
	"fmt"
	"time"
)

// Nothing was ever deleted from this database. Expired sessions and
// tokens were filtered on read and left in place; the audit log, the
// activity feed, webhook deliveries and the mail queue grew without
// bound. Sweep removes them (#122).

// Swept counts what one sweep removed, per table. Zero-valued entries are
// left in so a caller logging the result sees every table it asked about.
type Swept map[string]int64

// Total is how many rows the sweep removed altogether.
func (s Swept) Total() int64 {
	var n int64
	for _, v := range s {
		n += v
	}
	return n
}

// Retention says how long each capped table keeps a row. A zero duration
// means keep forever, which is what an instance that has not configured
// retention gets: growing is a decision, but so is deleting an audit
// trail, and the second one is not made on an operator's behalf.
type Retention struct {
	Audit             time.Duration
	Events            time.Duration
	WebhookDeliveries time.Duration
	Mail              time.Duration
}

// Sweep deletes expired sessions and tokens, then the rows older than
// each configured retention. Errors are returned with whatever was
// removed before them: a sweep that fails halfway has still done that
// much, and the next one picks up the rest.
func (s *Store) Sweep(r Retention, now time.Time) (Swept, error) {
	out := Swept{}
	// Dead the moment they expire, whatever retention says. A used login
	// token cannot be replayed and an expired session cannot authenticate,
	// so neither is evidence of anything.
	expired := []struct {
		table string
		where string
	}{
		{"web_sessions", "expires_at <= ?"},
		{"login_tokens", "expires_at <= ?"},
		{"email_tokens", "expires_at <= ?"},
	}
	for _, e := range expired {
		n, err := s.deleteBy("DELETE FROM "+e.table+" WHERE "+e.where, fmtTime(now))
		out[e.table] += n
		if err != nil {
			return out, fmt.Errorf("sweeping %s: %w", e.table, err)
		}
	}

	// Order matters: deliveries before events. webhook_deliveries.event_id
	// is ON DELETE CASCADE, so an event taken out from under a delivery
	// takes the delivery with it — including one still queued for retry.
	// Sweeping finished deliveries first, and skipping any event that
	// still has an unfinished one, keeps that from happening. The effect
	// is that a delivery is kept for the shorter of the two retentions,
	// which is the honest reading of "keep deliveries for N".
	aged := []struct {
		table string
		where string
		keep  time.Duration
	}{
		{"audit_log", "created_at < ?", r.Audit},
		// Only deliveries that have finished: one still being retried is
		// live state, however old its first attempt.
		{"webhook_deliveries", "created_at < ? AND (delivered_at IS NOT NULL OR failed_at IS NOT NULL)", r.WebhookDeliveries},
		{"events", `created_at < ? AND NOT EXISTS (
			SELECT 1 FROM webhook_deliveries d
			WHERE d.event_id = events.id AND d.delivered_at IS NULL AND d.failed_at IS NULL)`, r.Events},
		{"notifications", "created_at < ? AND (sent_at IS NOT NULL OR failed_at IS NOT NULL)", r.Mail},
	}
	for _, a := range aged {
		if a.keep <= 0 {
			continue
		}
		n, err := s.deleteBy("DELETE FROM "+a.table+" WHERE "+a.where, fmtTime(now.Add(-a.keep)))
		out[a.table] += n
		if err != nil {
			return out, fmt.Errorf("sweeping %s: %w", a.table, err)
		}
	}
	return out, nil
}

func (s *Store) deleteBy(q string, args ...any) (int64, error) {
	res, err := s.DB.Exec(q, args...)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}
