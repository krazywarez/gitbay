package e2e

import (
	"encoding/json"
	"net/url"
	"os"
	"strings"
	"testing"
)

// TestAccountSettingsWeb covers managing your own keys and addresses from a
// browser session. Public keys are the only credential-shaped input the web
// accepts; secrets and token minting stay on SSH.
func TestAccountSettingsWeb(t *testing.T) {
	inst := startInstanceWith(t, "[web]\nmode = \"accounts\"\n")
	aliceKey := inst.newKey(t, "alice")
	inst.admin(t, "admin", "user", "create", "alice",
		"--key", aliceKey+".pub", "--email", "alice@example.test", "--verified")

	out, _, code := inst.ssh(t, aliceKey, "", "web", "login", "--json")
	if code != 0 {
		t.Fatal("web login failed")
	}
	var env struct {
		Data struct {
			URL string `json:"url"`
		} `json:"data"`
	}
	json.Unmarshal([]byte(out), &env)
	browser := newBrowser(t)
	browserGet(t, browser, inst.base()+env.Data.URL[strings.Index(env.Data.URL, "/login"):])

	status, body := browserGet(t, browser, inst.base()+"/settings")
	if status != 200 {
		t.Fatalf("account settings: %d", status)
	}
	// The key that signed us in is listed, and its address shows verified.
	if !strings.Contains(body, "SHA256:") {
		t.Error("no SSH key fingerprint listed")
	}
	if !strings.Contains(body, "alice@example.test") || !strings.Contains(body, "verified") {
		t.Error("verified address not shown")
	}

	// Add a second key through the form, then confirm it over SSH — the
	// web write must land in the same place the CLI reads.
	second := inst.newKey(t, "alice2")
	raw, err := os.ReadFile(second + ".pub")
	if err != nil {
		t.Fatal(err)
	}
	pub := string(raw)
	if status, _ := browserPost(t, browser, inst.base()+"/settings", url.Values{
		"field": {"key-add"}, "key": {pub}, "scope": {"git"},
	}); status != 303 && status != 200 {
		t.Fatalf("key add: %d", status)
	}
	out, _, _ = inst.ssh(t, aliceKey, "", "keys", "list", "--json")
	if strings.Count(out, "SHA256:") != 2 || !strings.Contains(out, `"scope":"git"`) {
		t.Fatalf("key not registered with its scope: %s", out)
	}

	// A git-scoped key can move git data but cannot run commands, so the
	// scope the form set is really enforced.
	if _, _, code := inst.ssh(t, second, "", "whoami"); code == 0 {
		t.Error("git-scoped key ran a control command")
	}

	// Removing it through the form removes it for SSH too.
	fp := gitScopedFingerprint(t, out)
	if status, _ := browserPost(t, browser, inst.base()+"/settings", url.Values{
		"field": {"key-remove"}, "fingerprint": {fp},
	}); status != 303 && status != 200 {
		t.Fatalf("key remove: %d", status)
	}
	out, _, _ = inst.ssh(t, aliceKey, "", "keys", "list", "--json")
	if strings.Count(out, "SHA256:") != 1 {
		t.Fatalf("key not removed: %s", out)
	}

	// Garbage is refused by the same validation the CLI uses, and says so.
	// The redirect carries the message, so the followed page shows it.
	_, body = browserPost(t, browser, inst.base()+"/settings", url.Values{
		"field": {"key-add"}, "key": {"not a key"},
	})
	if !strings.Contains(body, `class="error"`) {
		t.Error("invalid key accepted without an error")
	}

	// Token minting is SSHOnly and has no web form to reach it.
	if strings.Contains(body, `value="token-mint"`) {
		t.Error("token minting exposed on the web")
	}
}

// gitScopedFingerprint pulls the fingerprint of the git-scoped key out of
// "auth keys list --json".
func gitScopedFingerprint(t *testing.T, blob string) string {
	t.Helper()
	var env struct {
		Data []struct {
			Fingerprint string `json:"fingerprint"`
			Scope       string `json:"scope"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(blob), &env); err != nil {
		t.Fatalf("keys list JSON: %v\n%s", err, blob)
	}
	for _, k := range env.Data {
		if k.Scope == "git" {
			return k.Fingerprint
		}
	}
	t.Fatalf("no git-scoped key in %s", blob)
	return ""
}
