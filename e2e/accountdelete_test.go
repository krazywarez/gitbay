package e2e

import (
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"
)

// Self-service deletion (#322): a request over SSH mails a link, the link
// schedules the purge and disables the account, a narrower key is refused
// and cannot cancel, and a full-scope key cancels by signing in.
func TestAccountDeleteFlow(t *testing.T) {
	t.Parallel()
	smtp := startFakeSMTP(t)
	inst := startInstanceWith(t, fmt.Sprintf(
		"[web]\nmode = \"accounts\"\n[mail]\nsmtp_host = %q\nfrom = \"noreply@gitbay.test\"\n",
		smtp.addr))
	key := inst.newKey(t, "erin")
	inst.admin(t, "admin", "user", "create", "erin", "--key", key+".pub",
		"--email", "erin@example.test", "--verified")
	gitKey := inst.newKey(t, "erin-git")
	pub, err := os.ReadFile(gitKey + ".pub")
	if err != nil {
		t.Fatal(err)
	}
	if _, errOut, code := inst.ssh(t, key, string(pub), "keys", "add", "--scope", "git"); code != 0 {
		t.Fatalf("keys add: %s", errOut)
	}

	if _, errOut, code := inst.ssh(t, key, "", "account", "delete", "--confirm", "erin"); code != 0 {
		t.Fatalf("account delete: exit %d %s", code, errOut)
	}
	mail := smtp.waitFor(t, "erin@example.test", "/settings/delete?token=")
	link := regexp.MustCompile(`/settings/delete\?token=[A-Za-z0-9_-]+`).FindString(mail)
	if link == "" {
		t.Fatalf("no link in mail:\n%s", mail)
	}

	browser := newBrowser(t)
	if status, body := browserGet(t, browser, inst.base()+link); status != 200 || !strings.Contains(body, "Delete erin") {
		t.Fatalf("GET link: %d", status)
	}
	if _, _, code := inst.ssh(t, key, "", "whoami"); code != 0 {
		t.Fatal("opening the page changed the account")
	}
	if status, body := browserPost(t, browser, inst.base()+link, url.Values{}); status != 200 || !strings.Contains(body, "Deletion scheduled") {
		t.Fatalf("POST link: %d\n%s", status, body)
	}

	if _, errOut, code := inst.ssh(t, gitKey, "", "whoami"); code != 4 || !strings.Contains(errOut, "scheduled for deletion") {
		t.Fatalf("git key while scheduled: exit %d %s", code, errOut)
	}
	if out := inst.admin(t, "admin", "user", "show", "erin", "--json"); !strings.Contains(out, `"delete_after"`) {
		t.Fatalf("show lacks the schedule:\n%s", out)
	}
	if _, errOut, code := inst.ssh(t, key, "", "whoami"); code != 0 || !strings.Contains(errOut, "deletion of your account was cancelled") {
		t.Fatalf("full key while scheduled: exit %d %s", code, errOut)
	}
	if out := inst.admin(t, "admin", "user", "show", "erin", "--json"); strings.Contains(out, `"delete_after"`) || !strings.Contains(out, `"state":"active"`) {
		t.Fatalf("not restored:\n%s", out)
	}
	if _, errOut, _ := inst.ssh(t, gitKey, "", "whoami"); strings.Contains(errOut, "scheduled for deletion") {
		t.Fatal("git key still told the account is scheduled after the cancel")
	}
}
