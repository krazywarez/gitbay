package push

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"gitbay.org/gitbay/internal/config"
)

// result is what one send means for the queue row.
type result int

const (
	resultSent  result = iota // delivered
	resultRetry               // transient; back off and try again
	resultReap                // Apple says the token is dead; drop the device
	resultDead                // permanent for this payload; dead-letter it
)

// maxBodyBytes keeps an alert inside APNs' 4KB payload limit with room
// for the rest of the JSON. A summary longer than this is cut rather
// than rejected.
const maxBodyBytes = 3000

type Client struct {
	http   *http.Client
	tokens *tokenSource
	key    *ecdsa.PrivateKey
	host   string
	scheme string
	topic  string
	// siteURL is this instance, as the alert reports it. With the
	// recipient's username it identifies the account a notice belongs
	// to, which a device signed in to several cannot otherwise tell.
	siteURL string
}

func NewClient(cfg config.Push, siteURL string) (*Client, error) {
	c := &Client{
		// stdlib negotiates HTTP/2 over ALPN, which is what APNs
		// requires; no explicit http2 transport is needed.
		http:    &http.Client{Timeout: 30 * time.Second},
		host:    cfg.Host(),
		scheme:  apnsScheme(),
		topic:   cfg.Topic,
		siteURL: siteURL,
	}
	if cfg.KeyFile != "" {
		key, err := config.LoadAPNSKey(cfg.KeyFile)
		if err != nil {
			return nil, err
		}
		c.key = key
		c.tokens = newTokenSource(key, cfg.KeyID, cfg.TeamID)
	}
	return c, nil
}

// apnsScheme is https for the real Apple hosts. GITBAY_APNS_HOST redirects
// the endpoint for tests (config.Push.Host), and the fake it points at
// speaks plain HTTP/1.1 rather than negotiating TLS, so the same override
// has to drop the scheme too, or every request fails with "server gave
// HTTP response to HTTPS client" instead of reaching the fake at all.
//
// The drop only applies to a host on this machine. The provider token is
// a bearer credential, valid for an hour and good for any device under
// the topic; putting it on the wire in cleartext to somewhere else is not
// a thing the test override should be able to arrange. Every fake in the
// tree is an httptest server, which always binds loopback, so nothing
// loses anything by the restriction. Either way the decision is logged,
// so an operator who set the variable learns what it did.
func apnsScheme() string {
	h := os.Getenv("GITBAY_APNS_HOST")
	if h == "" {
		return "https"
	}
	if !loopbackHost(h) {
		slog.Warn("push: GITBAY_APNS_HOST is not on this machine, still sending over HTTPS; the provider token is a bearer credential and does not travel in cleartext", "host", h)
		return "https"
	}
	slog.Warn("push: GITBAY_APNS_HOST is set, sending to it over plain HTTP instead of APNs", "host", h)
	return "http"
}

// loopbackHost reports whether a host:port names this machine. The port
// is optional: config.Push.Host returns a bare hostname for the real
// endpoints, and the override may or may not carry one.
func loopbackHost(hostport string) bool {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Send delivers one alert. The returned duration is the server's
// Retry-After when it gave one, zero otherwise.
func (c *Client) Send(ctx context.Context, token, user string, badge int, title, body, path string) (result, time.Duration, error) {
	if len(body) > maxBodyBytes {
		// A raw byte cut can land mid-rune on multi-byte UTF-8 (emoji,
		// accents, non-Latin usernames). ToValidUTF8 drops the
		// resulting dangling bytes instead of leaving them for
		// encoding/json to turn into a garbled U+FFFD.
		body = strings.ToValidUTF8(body[:maxBodyBytes], "")
	}
	payload, err := json.Marshal(map[string]any{
		"aps": map[string]any{
			"alert":     map[string]string{"title": title, "body": body},
			"sound":     "default",
			"badge":     badge,
			"thread-id": title,
		},
		"path": path,
		// Which account this is for. A device token is one install, and
		// an install registers against every account signed in on it, so
		// path alone is ambiguous — two instances can hold the same
		// owner/name. Together these are the account's identity.
		"instance": c.siteURL,
		"user":     user,
	})
	if err != nil {
		return resultDead, 0, err
	}
	bearer, err := c.tokens.token()
	if err != nil {
		return resultRetry, 0, err
	}
	url := c.scheme + "://" + c.host + "/3/device/" + token
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return resultDead, 0, err
	}
	req.Header.Set("authorization", "bearer "+bearer)
	req.Header.Set("apns-topic", c.topic)
	req.Header.Set("apns-push-type", "alert")
	req.Header.Set("apns-priority", "10")
	req.Header.Set("content-type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return resultRetry, 0, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))

	var apnsErr struct {
		Reason string `json:"reason"`
	}
	json.Unmarshal(raw, &apnsErr)

	var after time.Duration
	if v := resp.Header.Get("Retry-After"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			after = time.Duration(n) * time.Second
		}
	}

	switch {
	case resp.StatusCode == http.StatusOK:
		return resultSent, 0, nil
	case resp.StatusCode == http.StatusGone,
		apnsErr.Reason == "BadDeviceToken",
		apnsErr.Reason == "Unregistered":
		// Apple is authoritative about which tokens are live.
		return resultReap, 0, fmt.Errorf("apns %d %s", resp.StatusCode, apnsErr.Reason)
	case resp.StatusCode == http.StatusTooManyRequests, resp.StatusCode >= 500:
		return resultRetry, after, fmt.Errorf("apns %d %s", resp.StatusCode, apnsErr.Reason)
	default:
		// Retrying a rejected payload will not fix it.
		return resultDead, 0, fmt.Errorf("apns %d %s", resp.StatusCode, apnsErr.Reason)
	}
}
