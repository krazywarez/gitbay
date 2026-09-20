package push

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"gitbay.org/gitbay/internal/config"
)

// fakeAPNs stands in for Apple. It speaks HTTP/1.1; the real transport is
// h2 by ALPN, which is stdlib behaviour and not this repository's to test.
func fakeAPNs(t *testing.T, h http.HandlerFunc) (*Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	t.Setenv("GITBAY_APNS_HOST", strings.TrimPrefix(srv.URL, "http://"))
	c, err := NewClient(config.Push{
		Enabled: true, KeyID: "K", TeamID: "T",
		Topic: "org.gitbay.gitbay", Environment: "production",
	})
	if err != nil {
		t.Fatal(err)
	}
	c.key = testKey(t)
	c.tokens = newTokenSource(c.key, "K", "T")
	c.scheme = "http"
	return c, srv
}

func TestSendShapesTheRequest(t *testing.T) {
	var gotPath, gotTopic, gotType, gotAuth string
	var payload map[string]any
	c, _ := fakeAPNs(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotTopic = r.URL.Path, r.Header.Get("apns-topic")
		gotType, gotAuth = r.Header.Get("apns-push-type"), r.Header.Get("authorization")
		raw, _ := io.ReadAll(r.Body)
		json.Unmarshal(raw, &payload)
		w.WriteHeader(200)
	})
	res, _, err := c.Send(context.Background(), "DEVTOKEN", "krz/gitbay", "cmc opened issue #12", "krz/gitbay/issues/12")
	if err != nil || res != resultSent {
		t.Fatalf("res = %v, err = %v", res, err)
	}
	if gotPath != "/3/device/DEVTOKEN" {
		t.Fatalf("path = %q", gotPath)
	}
	if gotTopic != "org.gitbay.gitbay" || gotType != "alert" {
		t.Fatalf("topic = %q, push-type = %q", gotTopic, gotType)
	}
	if !strings.HasPrefix(gotAuth, "bearer ") {
		t.Fatalf("authorization = %q", gotAuth)
	}
	aps := payload["aps"].(map[string]any)
	alert := aps["alert"].(map[string]any)
	if alert["title"] != "krz/gitbay" || alert["body"] != "cmc opened issue #12" {
		t.Fatalf("alert = %v", alert)
	}
	if aps["thread-id"] != "krz/gitbay" {
		t.Fatalf("thread-id = %v", aps["thread-id"])
	}
	if payload["path"] != "krz/gitbay/issues/12" {
		t.Fatalf("path = %v", payload["path"])
	}
	// Collapsing is wrong here: two comments are two notices.
	if _, ok := payload["apns-collapse-id"]; ok {
		t.Fatal("collapse id set")
	}
}

func TestSendMapsResponses(t *testing.T) {
	cases := []struct {
		name       string
		status     int
		body       string
		retryAfter string
		want       result
		wantAfter  time.Duration
	}{
		{"ok", 200, "", "", resultSent, 0},
		{"gone", 410, `{"reason":"Unregistered"}`, "", resultReap, 0},
		{"bad token", 400, `{"reason":"BadDeviceToken"}`, "", resultReap, 0},
		{"other 400 is permanent", 400, `{"reason":"PayloadTooLarge"}`, "", resultDead, 0},
		{"forbidden is permanent", 403, `{"reason":"InvalidProviderToken"}`, "", resultDead, 0},
		{"too many requests retries", 429, `{"reason":"TooManyRequests"}`, "7", resultRetry, 7 * time.Second},
		{"server error retries", 503, `{"reason":"ServiceUnavailable"}`, "", resultRetry, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := fakeAPNs(t, func(w http.ResponseWriter, r *http.Request) {
				if tc.retryAfter != "" {
					w.Header().Set("Retry-After", tc.retryAfter)
				}
				w.WriteHeader(tc.status)
				io.WriteString(w, tc.body)
			})
			res, after, err := c.Send(context.Background(), "T", "t", "b", "p")
			// Only a delivered push has no error. Every other result
			// carries the status and reason, which is what the drainer
			// records on the queue row.
			if tc.want == resultSent && err != nil {
				t.Fatalf("err = %v", err)
			}
			if tc.want != resultSent && err == nil {
				t.Fatalf("want an error explaining %v, got nil", tc.want)
			}
			if res != tc.want {
				t.Fatalf("res = %v, want %v", res, tc.want)
			}
			if after != tc.wantAfter {
				t.Fatalf("retryAfter = %v, want %v", after, tc.wantAfter)
			}
		})
	}
}
