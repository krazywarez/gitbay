package e2e

import (
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"
)

// notifications settings mail off keeps the inbox and stops activity
// mail; on brings it back. The account page carries the same switch
// (#194).
func TestMailPreference(t *testing.T) {
	smtp := startFakeSMTP(t)
	inst := startInstanceWith(t, fmt.Sprintf(
		"[mail]\nsmtp_host = %q\nfrom = \"noreply@gitbay.test\"\n[web]\nmode = \"accounts\"\n", smtp.addr))
	aliceKey := inst.newKey(t, "alice")
	bobKey := inst.newKey(t, "bob")
	inst.admin(t, "admin", "user", "create", "alice",
		"--key", aliceKey+".pub", "--email", "alice@example.test", "--verified")
	inst.admin(t, "admin", "user", "create", "bob", "--key", bobKey+".pub")
	if _, errOut, code := inst.ssh(t, aliceKey, "", "repo", "create", "alice/app"); code != 0 {
		t.Fatalf("repo create: %s", errOut)
	}

	out, _, code := inst.ssh(t, aliceKey, "", "notifications", "settings", "show", "--json")
	if code != 0 || !strings.Contains(out, `"mail":true`) {
		t.Fatalf("settings show: %d %s", code, out)
	}
	if _, _, code := inst.ssh(t, aliceKey, "", "notifications", "settings", "mail", "sometimes"); code != 2 {
		t.Fatalf("bad value accepted: %d", code)
	}
	if out, _, code := inst.ssh(t, aliceKey, "", "notifications", "settings", "mail", "off", "--json"); code != 0 || !strings.Contains(out, `"mail":false`) {
		t.Fatalf("mail off: %d %s", code, out)
	}

	// Mail off: the inbox row is filed, no mail goes out.
	if _, errOut, code := inst.ssh(t, bobKey, "", "issue", "create", "alice/app", "--title", "'leak'"); code != 0 {
		t.Fatalf("issue create: %s", errOut)
	}
	rows := notices(t, inst, aliceKey)
	if len(rows) != 1 || !strings.Contains(rows[0].Summary, "opened issue #1") {
		t.Fatalf("inbox with mail off: %+v", rows)
	}
	time.Sleep(5 * time.Second) // the mailer ticks every two seconds
	if got := smtp.mailTo("alice@example.test"); len(got) != 0 {
		t.Fatalf("mail sent with the preference off:\n%s", got[0])
	}

	// The account page shows it off and turns it back on.
	alice := inst.login(t, aliceKey)
	set := inst.base() + "/settings"
	if _, body := browserGet(t, alice, set); !strings.Contains(body, `name="mail" value="on">`) || strings.Contains(body, `name="mail" value="on" checked`) {
		t.Fatalf("account page does not show mail off:\n%s", body)
	}
	if status, _ := browserPost(t, alice, set, url.Values{"field": {"notify-mail"}, "mail": {"on"}}); status != 200 {
		t.Fatalf("settings post: %d", status)
	}
	if out, _, _ := inst.ssh(t, aliceKey, "", "notifications", "settings", "show", "--json"); !strings.Contains(out, `"mail":true`) {
		t.Fatalf("web toggle did not turn mail on: %s", out)
	}
	if _, errOut, code := inst.ssh(t, bobKey, "", "issue", "create", "alice/app", "--title", "'still leaking'"); code != 0 {
		t.Fatalf("second issue: %s", errOut)
	}
	if m := smtp.waitFor(t, "alice@example.test", "still leaking"); !strings.Contains(m, "bob opened issue #2") {
		t.Fatalf("mail after turning it on:\n%s", m)
	}
}
