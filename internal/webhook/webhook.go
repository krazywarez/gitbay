// Package webhook delivers events to registered endpoints: HMAC-signed
// JSON POSTs with bounded retries, exponential backoff, and dead-lettering.
package webhook

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"time"

	"gitbay.org/gitbay/internal/store"
)

// ValidateURL rejects URLs a webhook must not target: non-HTTP schemes and,
// unless allowLocal, anything resolving to loopback, private, or link-local
// addresses (SSRF).
func ValidateURL(raw string, allowLocal bool) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("webhook URLs must be http or https")
	}
	if u.Hostname() == "" {
		return fmt.Errorf("webhook URL has no host")
	}
	if allowLocal {
		return nil
	}
	ips, err := net.LookupIP(u.Hostname())
	if err != nil {
		return fmt.Errorf("cannot resolve %s: %w", u.Hostname(), err)
	}
	for _, ip := range ips {
		if isForbidden(ip) {
			return fmt.Errorf("webhook target %s resolves to a private or local address; refusing (SSRF)", u.Hostname())
		}
	}
	return nil
}

func isForbidden(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsUnspecified()
}

type Deliverer struct {
	St         *store.Store
	AllowLocal bool
	RetryBase  time.Duration // first retry delay; doubles per attempt
	MaxAttempts int
	client     *http.Client
}

// New builds a deliverer whose dialer re-checks resolved addresses at
// connect time, so a DNS answer that changes after ValidateURL still cannot
// reach private space.
func New(st *store.Store, allowLocal bool, retryBase time.Duration) *Deliverer {
	d := &Deliverer{St: st, AllowLocal: allowLocal, RetryBase: retryBase, MaxAttempts: 5}
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	d.client = &http.Client{
		Timeout: 10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse // never follow redirects
		},
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				host, port, err := net.SplitHostPort(addr)
				if err != nil {
					return nil, err
				}
				ips, err := net.DefaultResolver.LookupIP(ctx, "ip", host)
				if err != nil {
					return nil, err
				}
				for _, ip := range ips {
					if !allowLocal && isForbidden(ip) {
						return nil, fmt.Errorf("refusing connection to private address %s", ip)
					}
				}
				return dialer.DialContext(ctx, network, net.JoinHostPort(ips[0].String(), port))
			},
		},
	}
	return d
}

// Run polls for due deliveries until ctx is done.
func (d *Deliverer) Run(ctx context.Context) {
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			due, err := d.St.DueDeliveries(20)
			if err != nil {
				slog.Error("webhook: listing due deliveries", "err", err)
				continue
			}
			for _, dl := range due {
				d.deliver(ctx, dl)
			}
		}
	}
}

type payload struct {
	Event     string          `json:"event"`
	Repo      string          `json:"repo"`
	Actor     string          `json:"actor,omitempty"`
	CreatedAt string          `json:"created_at"`
	Data      json.RawMessage `json:"data"`
}

func (d *Deliverer) deliver(ctx context.Context, dl store.Delivery) {
	body, err := json.Marshal(payload{
		Event: dl.EventKind, Repo: dl.RepoPath, Actor: dl.Actor,
		CreatedAt: dl.EventAt, Data: json.RawMessage(dl.DataJSON),
	})
	if err != nil {
		d.fail(dl, 0, "marshal: "+err.Error())
		return
	}
	req, err := http.NewRequestWithContext(ctx, "POST", dl.URL, bytes.NewReader(body))
	if err != nil {
		d.fail(dl, 0, "request: "+err.Error())
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "gitbay-webhook")
	req.Header.Set("X-Gitbay-Event", dl.EventKind)
	req.Header.Set("X-Gitbay-Delivery", fmt.Sprint(dl.ID))
	if dl.Secret != "" {
		mac := hmac.New(sha256.New, []byte(dl.Secret))
		mac.Write(body)
		req.Header.Set("X-Gitbay-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	}

	resp, err := d.client.Do(req)
	if err != nil {
		d.fail(dl, 0, err.Error())
		return
	}
	io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		if err := d.St.MarkDelivered(dl.ID, resp.StatusCode); err != nil {
			slog.Error("webhook: marking delivered", "err", err)
		}
		return
	}
	d.fail(dl, resp.StatusCode, fmt.Sprintf("endpoint returned %d", resp.StatusCode))
}

// fail schedules a retry with exponential backoff, dead-lettering after
// MaxAttempts.
func (d *Deliverer) fail(dl store.Delivery, status int, msg string) {
	attempt := dl.Attempts + 1 // the one that just happened
	if attempt >= d.MaxAttempts {
		if err := d.St.MarkAttemptFailed(dl.ID, status, msg, nil); err != nil {
			slog.Error("webhook: dead-lettering", "err", err)
		}
		slog.Warn("webhook dead-lettered", "delivery", dl.ID, "url", dl.URL, "err", msg)
		return
	}
	next := time.Now().Add(d.RetryBase << (attempt - 1))
	if err := d.St.MarkAttemptFailed(dl.ID, status, msg, &next); err != nil {
		slog.Error("webhook: scheduling retry", "err", err)
	}
}
