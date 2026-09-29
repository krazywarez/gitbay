package e2e

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"strings"
	"testing"
)

// TestWebhookSecretFromTheSettingsPage adds a webhook from the settings
// page with a secret. The secret signs deliveries, and appears nowhere on
// the resulting pages or in the command's listing (#296).
func TestWebhookSecretFromTheSettingsPage(t *testing.T) {
	t.Parallel()
	inst := startInstanceWith(t, "[web]\nmode = \"accounts\"\n[webhooks]\nallow_local = true\n")
	aliceKey := inst.newKey(t, "alice")
	inst.admin(t, "admin", "user", "create", "alice",
		"--key", aliceKey+".pub", "--email", "alice@example.test", "--verified")
	if _, errOut, code := inst.ssh(t, aliceKey, "", "repo", "create", "alice/proj"); code != 0 {
		t.Fatalf("repo create: %s", errOut)
	}
	alice := inst.login(t, aliceKey)
	recv := startHookReceiver(t)
	const secret = "form-secret-9d2f"

	status, body := browserPost(t, alice, inst.base()+"/alice/proj/settings", url.Values{
		"field": {"webhook-add"}, "url": {"http://" + recv.addr + "/hook"},
		"events": {"issue.created"}, "secret": {secret},
	})
	if status != 200 || strings.Contains(body, secret) || !strings.Contains(body, "signed") {
		t.Fatalf("add: %d\n%s", status, body)
	}
	if _, body = browserGet(t, alice, inst.base()+"/alice/proj/settings"); strings.Contains(body, secret) {
		t.Fatal("secret on the settings page")
	}
	out, _, _ := inst.ssh(t, aliceKey, "", "webhook", "list", "alice/proj", "--json")
	if strings.Contains(out, secret) {
		t.Fatalf("secret in webhook list: %s", out)
	}

	if _, errOut, code := inst.ssh(t, aliceKey, "", "issue", "create", "alice/proj", "--title", "'hook me'"); code != 0 {
		t.Fatalf("issue create: %s", errOut)
	}
	h := recv.waitN(t, 1)[0]
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(h.body)
	if h.signature != "sha256="+hex.EncodeToString(mac.Sum(nil)) {
		t.Fatalf("HMAC mismatch: %s", h.signature)
	}
}
