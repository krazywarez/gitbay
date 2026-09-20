package push

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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
}

func NewClient(cfg config.Push) (*Client, error) {
	c := &Client{
		// stdlib negotiates HTTP/2 over ALPN, which is what APNs
		// requires; no explicit http2 transport is needed.
		http:   &http.Client{Timeout: 30 * time.Second},
		host:   cfg.Host(),
		scheme: "https",
		topic:  cfg.Topic,
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

// Send delivers one alert. The returned duration is the server's
// Retry-After when it gave one, zero otherwise.
func (c *Client) Send(ctx context.Context, token, title, body, path string) (result, time.Duration, error) {
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
			"thread-id": title,
		},
		"path": path,
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
