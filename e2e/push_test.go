package e2e

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// writeTestAPNSKey writes a P-256 PKCS#8 key PEM, as config validation
// expects for [push] key_file. Modelled on writeP8 in
// internal/config/config_test.go, which is in a different package and so
// cannot be called directly.
func writeTestAPNSKey(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "apns.p8")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := pem.Encode(f, &pem.Block{Type: "PRIVATE KEY", Bytes: der}); err != nil {
		t.Fatal(err)
	}
	return p
}

// A push reaches a registered device with the same words the inbox row
// carries, and a token Apple has retired takes its device with it.
func TestPush(t *testing.T) {
	var mu sync.Mutex
	var got []map[string]any
	var gone bool

	apns := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var payload map[string]any
		json.Unmarshal(raw, &payload)
		mu.Lock()
		defer mu.Unlock()
		if gone {
			w.WriteHeader(410)
			io.WriteString(w, `{"reason":"Unregistered"}`)
			return
		}
		got = append(got, payload)
		w.WriteHeader(200)
	}))
	defer apns.Close()

	keyPath := writeTestAPNSKey(t)
	t.Setenv("GITBAY_APNS_HOST", strings.TrimPrefix(apns.URL, "http://"))
	inst := startInstanceWith(t, `[push]
enabled = true
key_file = "`+keyPath+`"
key_id = "KEYID"
team_id = "TEAMID"
topic = "org.gitbay.gitbay"
environment = "production"
`)

	aliceKey := inst.newKey(t, "alice")
	bobKey := inst.newKey(t, "bob")
	inst.admin(t, "admin", "user", "create", "alice", "--key", aliceKey+".pub")
	inst.admin(t, "admin", "user", "create", "bob", "--key", bobKey+".pub")
	if _, errOut, code := inst.ssh(t, aliceKey, "", "repo", "create", "alice/app"); code != 0 {
		t.Fatalf("repo create: %s", errOut)
	}

	// Bob watches alice's repository and registers a device.
	if out, errOut, code := inst.ssh(t, bobKey, "", "repo", "watch", "alice/app"); code != 0 {
		t.Fatalf("watch: %s%s", out, errOut)
	}
	if out, errOut, code := inst.ssh(t, bobKey, "DEVTOKEN\n", "notifications", "device", "add", "--label", "iphone"); code != 0 {
		t.Fatalf("device add: %s%s", out, errOut)
	}
	if out, _, _ := inst.ssh(t, bobKey, "", "notifications", "device", "list", "--json"); !strings.Contains(out, `"label":"iphone"`) {
		t.Fatalf("device not listed:\n%s", out)
	} else if strings.Contains(out, "DEVTOKEN") {
		t.Fatalf("device list printed the token in full:\n%s", out)
	}

	// Alice opens an issue. Bob hears about it.
	// The server tokenizer splits the ssh command string on whitespace, so
	// a multi-word flag value needs its own quoting (internal/protocol.Tokenize).
	if out, errOut, code := inst.ssh(t, aliceKey, "", "issue", "create", "alice/app", "--title", "'a bug'", "--body", "x"); code != 0 {
		t.Fatalf("issue create: %s%s", out, errOut)
	}

	waitFor(t, "a push to arrive", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(got) == 1
	})

	mu.Lock()
	aps := got[0]["aps"].(map[string]any)
	alert := aps["alert"].(map[string]any)
	mu.Unlock()
	if alert["title"] != "alice/app" {
		t.Fatalf("title = %v", alert["title"])
	}
	// The same words the inbox row carries.
	if body, _ := alert["body"].(string); !strings.Contains(body, "opened issue #1") {
		t.Fatalf("body = %q", body)
	}
	if got[0]["path"] != "alice/app/issues/1" {
		t.Fatalf("path = %v", got[0]["path"])
	}
	// The account the notice is for. A device signed in to several
	// accounts cannot tell from the path alone which one this is.
	if got[0]["user"] != "bob" {
		t.Fatalf("user = %v", got[0]["user"])
	}
	if inst, _ := got[0]["instance"].(string); !strings.HasPrefix(inst, "https://") {
		t.Fatalf("instance = %v, want this instance's site_url", inst)
	}

	// Apple retires the token. The next push reaps the device.
	mu.Lock()
	gone = true
	mu.Unlock()
	if out, errOut, code := inst.ssh(t, aliceKey, "", "issue", "comment", "alice/app", "1", "--message", "'ping'"); code != 0 {
		t.Fatalf("issue comment: %s%s", out, errOut)
	}
	waitFor(t, "the device to be reaped after a 410", func() bool {
		// This is an absence check, unlike every other waitFor in the
		// suite: a transient ssh failure returns empty stdout, which
		// would otherwise read as a false "reaped". The exit code rules
		// that out.
		out, _, code := inst.ssh(t, bobKey, "", "notifications", "device", "list", "--json")
		return code == 0 && !strings.Contains(out, "iphone")
	})

	// The inbox is untouched by any of it: push is a side channel.
	if out, _, _ := inst.ssh(t, bobKey, "", "notifications", "list", "--json"); !strings.Contains(out, "opened issue #1") {
		t.Fatalf("inbox missing the notice:\n%s", out)
	}
}
