// Package notify drains the notification queue: activity mail with the same
// bounded-retry discipline as webhook delivery, so a flaky relay delays
// feedback instead of losing it.
package notify

import (
	"context"
	"log/slog"
	"time"

	"gitbay.org/gitbay/internal/config"
	"gitbay.org/gitbay/internal/mail"
	"gitbay.org/gitbay/internal/store"
)

type Mailer struct {
	St          *store.Store
	Cfg         config.Config
	RetryBase   time.Duration
	MaxAttempts int
}

func New(st *store.Store, cfg config.Config, retryBase time.Duration) *Mailer {
	return &Mailer{St: st, Cfg: cfg, RetryBase: retryBase, MaxAttempts: 5}
}

// Run polls for due mail until ctx is done.
func (m *Mailer) Run(ctx context.Context) {
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			due, err := m.St.DueMail(20)
			if err != nil {
				slog.Error("notify: listing due mail", "err", err)
				continue
			}
			for _, q := range due {
				if err := mail.Send(m.Cfg, q.Recipient, q.Subject, q.Body); err != nil {
					attempt := q.Attempts + 1
					if attempt >= m.MaxAttempts {
						m.St.MarkMailFailed(q.ID, err.Error(), nil)
						slog.Warn("notification dead-lettered", "recipient", q.Recipient, "err", err)
					} else {
						next := time.Now().Add(m.RetryBase << (attempt - 1))
						m.St.MarkMailFailed(q.ID, err.Error(), &next)
					}
					continue
				}
				m.St.MarkMailSent(q.ID)
			}
		}
	}
}
