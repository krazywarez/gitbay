// Package notify drains the notification queue: activity mail with the same
// bounded-retry discipline as webhook delivery, so a flaky relay delays
// feedback instead of losing it.
package notify

import (
	"context"
	"log/slog"
	"regexp"
	"time"

	"gitbay.org/gitbay/internal/config"
	"gitbay.org/gitbay/internal/mail"
	"gitbay.org/gitbay/internal/store"
)

// DefaultMaxAttempts and DefaultRetryBase are the retry parameters gitbayd
// wires up in production (cmd/gitbayd/main.go), named so anything that
// needs to reason about the mailer's worst-case delivery time — such as
// checking it against a login link's TTL — computes it from the numbers
// actually in force rather than a copy of them.
const (
	DefaultMaxAttempts = 5
	DefaultRetryBase   = 30 * time.Second
)

type Mailer struct {
	St          *store.Store
	Cfg         config.Config
	RetryBase   time.Duration
	MaxAttempts int
}

func New(st *store.Store, cfg config.Config, retryBase time.Duration) *Mailer {
	return &Mailer{St: st, Cfg: cfg, RetryBase: retryBase, MaxAttempts: DefaultMaxAttempts}
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
						slog.Warn("notification dead-lettered",
							"mail", q.ID, "attempts", attempt, "err", redactAddresses(err.Error()))
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

// A relay's rejection usually quotes the address it rejected — "550 5.1.1
// <x@y>: Recipient address rejected" — so dropping the recipient field
// alone would not keep an address out of the log.
var addressPat = regexp.MustCompile(`[^\s<>@,;:"]+@[^\s<>@,;:"]+`)

// redactAddresses removes mail addresses from text bound for the log. The
// unredacted error is still recorded on the queue row, where an instance
// admin reads it on /admin: the database holds the address, the log does
// not (#173).
func redactAddresses(s string) string {
	return addressPat.ReplaceAllString(s, "<address>")
}
