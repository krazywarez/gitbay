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
	"unicode/utf8"

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
	}, "https://gitbay.example")
	if err != nil {
		t.Fatal(err)
	}
	c.key = testKey(t)
	c.tokens = newTokenSource(c.key, "K", "T")
	c.scheme = "http"
	return c, srv
}

func TestSendShapesTheRequest(t *testing.T) {
	var gotPath, gotTopic, gotType, gotAuth, gotCollapse string
	var payload map[string]any
	c, _ := fakeAPNs(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotTopic = r.URL.Path, r.Header.Get("apns-topic")
		gotType, gotAuth = r.Header.Get("apns-push-type"), r.Header.Get("authorization")
		gotCollapse = r.Header.Get("apns-collapse-id")
		raw, _ := io.ReadAll(r.Body)
		json.Unmarshal(raw, &payload)
		w.WriteHeader(200)
	})
	res, _, err := c.Send(context.Background(), "DEVTOKEN", "cmc", "krz/gitbay", "cmc opened issue #12", "krz/gitbay/issues/12")
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
	// Collapsing is wrong here: two comments are two notices. This is an
	// APNs HTTP header, not a body field, so it must be checked on the
	// request the handler received, not on the decoded JSON payload.
	if gotCollapse != "" {
		t.Fatalf("apns-collapse-id = %q, want unset", gotCollapse)
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
			res, after, err := c.Send(context.Background(), "T", "u", "t", "b", "p")
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

func TestSendTruncatesBodyOnRuneBoundary(t *testing.T) {
	var payload map[string]any
	c, _ := fakeAPNs(t, func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		json.Unmarshal(raw, &payload)
		w.WriteHeader(200)
	})
	// A leading ASCII byte shifts every following two-byte rune off an
	// even offset, so a raw cut at maxBodyBytes is guaranteed to land on
	// the second byte of one of them rather than a rune boundary.
	long := "x" + strings.Repeat("é", 2000)
	res, _, err := c.Send(context.Background(), "T", "u", "t", long, "p")
	if err != nil || res != resultSent {
		t.Fatalf("res = %v, err = %v", res, err)
	}
	aps := payload["aps"].(map[string]any)
	alert := aps["alert"].(map[string]any)
	body := alert["body"].(string)
	if !utf8.ValidString(body) {
		t.Fatalf("body is not valid UTF-8: %q", body)
	}
	if len(body) > maxBodyBytes {
		t.Fatalf("body is %d bytes, want <= %d", len(body), maxBodyBytes)
	}
}

// The override drops to plain HTTP only for a host on this machine. The
// provider token is a bearer credential valid for an hour that can push
// to any device under the topic, so a GITBAY_APNS_HOST aimed anywhere
// else keeps HTTPS rather than putting it on the wire in cleartext.
func TestAPNSSchemeDowngradesOnlyOnLoopback(t *testing.T) {
	for _, tc := range []struct{ host, want string }{
		{"", "https"},
		{"127.0.0.1:8080", "http"},
		{"127.0.0.53:2197", "http"},
		{"localhost:1234", "http"},
		{"[::1]:1234", "http"},
		{"::1", "http"},
		{"10.0.0.5:2197", "https"},
		{"apns.example.com", "https"},
		{"api.push.apple.com:443", "https"},
		{"not a host", "https"},
	} {
		t.Setenv("GITBAY_APNS_HOST", tc.host)
		if got := apnsScheme(); got != tc.want {
			t.Errorf("apnsScheme() with host %q = %q, want %q", tc.host, got, tc.want)
		}
	}
}

// The alert names the account it belongs to. One device token is one
// install, and an install registers against every account signed in on
// it, so `path` alone cannot say which instance a notice came from — two
// instances can hold the same owner/name.
func TestSendNamesTheAccount(t *testing.T) {
	var payload map[string]any
	c, _ := fakeAPNs(t, func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		json.Unmarshal(raw, &payload)
		w.WriteHeader(200)
	})
	c.siteURL = "https://gitbay.org"

	if _, _, err := c.Send(context.Background(), "DEVTOKEN", "cmc",
		"krz/gitbay", "cmc opened issue #12", "krz/gitbay/issues/12"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if payload["instance"] != "https://gitbay.org" {
		t.Fatalf("instance = %v", payload["instance"])
	}
	if payload["user"] != "cmc" {
		t.Fatalf("user = %v", payload["user"])
	}
	// Still carries what it always did.
	if payload["path"] != "krz/gitbay/issues/12" {
		t.Fatalf("path = %v", payload["path"])
	}
}
