package push

import (
	"context"
	"log/slog"
	"time"

	"gitbay.org/gitbay/internal/config"
	"gitbay.org/gitbay/internal/store"
)

// DefaultMaxAttempts matches the mailer's: a flaky APNs delays a
// notification rather than losing it, up to a point.
const DefaultMaxAttempts = 5

type Deliverer struct {
	St          *store.Store
	Cl          *Client
	RetryBase   time.Duration
	MaxAttempts int
}

func New(st *store.Store, cfg config.Push, retryBase time.Duration) (*Deliverer, error) {
	cl, err := NewClient(cfg)
	if err != nil {
		return nil, err
	}
	return &Deliverer{St: st, Cl: cl, RetryBase: retryBase, MaxAttempts: DefaultMaxAttempts}, nil
}

// Run drains the push queue until ctx is done.
func (d *Deliverer) Run(ctx context.Context) {
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			d.drain(ctx)
		}
	}
}

func (d *Deliverer) drain(ctx context.Context) {
	due, err := d.St.DuePush(20)
	if err != nil {
		slog.Error("push: listing due", "err", err)
		return
	}
	for _, q := range due {
		res, after, sendErr := d.Cl.Send(ctx, q.Token, q.Title, q.Body, q.Path)
		msg := ""
		if sendErr != nil {
			msg = sendErr.Error()
		}
		switch res {
		case resultSent:
			d.St.MarkPushSent(q.ID)
		case resultReap:
			// The queued rows cascade with the device.
			if err := d.St.DeletePushDeviceByToken(q.Token); err != nil {
				slog.Error("push: reaping device", "device", q.DeviceID, "err", err)
			}
		case resultRetry:
			attempt := q.Attempts + 1
			if attempt >= d.MaxAttempts {
				d.St.MarkPushFailed(q.ID, msg, nil)
				// The device id, never the token.
				slog.Warn("push dead-lettered",
					"push", q.ID, "device", q.DeviceID, "attempts", attempt, "err", msg)
				continue
			}
			wait := after
			if wait == 0 {
				wait = d.RetryBase << (attempt - 1)
			}
			next := time.Now().Add(wait)
			d.St.MarkPushFailed(q.ID, msg, &next)
		default: // resultDead
			d.St.MarkPushFailed(q.ID, msg, nil)
			slog.Warn("push rejected", "push", q.ID, "device", q.DeviceID, "err", msg)
		}
	}
}
