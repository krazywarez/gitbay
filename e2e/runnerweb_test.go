package e2e

import (
	"net/url"
	"os"
	"strings"
	"testing"
)

// The settings page attaches and detaches runners through the same
// commands the CLI uses, and lists what is attached.
func TestRunnerSettingsWeb(t *testing.T) {
	inst := startInstanceWith(t, "[web]\nmode = \"accounts\"\n")
	aliceKey := inst.newKey(t, "alice")
	inst.admin(t, "admin", "user", "create", "alice",
		"--key", aliceKey+".pub", "--email", "alice@example.test", "--verified")
	if _, errOut, code := inst.ssh(t, aliceKey, "", "repo", "create", "alice/app"); code != 0 {
		t.Fatalf("repo create: %s", errOut)
	}
	runnerKey := inst.newKey(t, "laptop")
	pub, _ := os.ReadFile(runnerKey + ".pub")

	alice := inst.login(t, aliceKey)
	settings := inst.base() + "/alice/app/settings"
	_, body := browserGet(t, alice, settings)
	if !strings.Contains(body, "No runners attached") {
		t.Fatalf("empty state missing:\n%s", body)
	}
	if status, _ := browserPost(t, alice, settings, url.Values{"field": {"runner-add"}, "key": {string(pub)}}); status != 200 {
		t.Fatalf("runner-add post: %d", status)
	}
	out, _, _ := inst.ssh(t, aliceKey, "", "repo", "runner", "list", "alice/app", "--json")
	if !strings.Contains(out, `"fingerprint":"SHA256:`) {
		t.Fatalf("not attached after the form: %s", out)
	}
	fp := out[strings.Index(out, "SHA256:"):]
	fp = fp[:strings.Index(fp, `"`)]
	// html/template writes + as &#43; in text and attributes, and a
	// fingerprint is base64, so the page shows the escaped form.
	shown := strings.ReplaceAll(fp, "+", "&#43;")
	_, body = browserGet(t, alice, settings)
	if !strings.Contains(body, shown) || !strings.Contains(body, `value="runner-remove"`) {
		t.Fatalf("attached runner not listed:\n%s", body)
	}
	if status, _ := browserPost(t, alice, settings, url.Values{"field": {"runner-remove"}, "fingerprint": {fp}}); status != 200 {
		t.Fatalf("runner-remove post: %d", status)
	}
	if out, _, _ = inst.ssh(t, aliceKey, "", "repo", "runner", "list", "alice/app", "--json"); strings.Contains(out, fp) {
		t.Fatalf("still attached after remove: %s", out)
	}
}
