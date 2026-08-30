package e2e

import (
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
)

func TestWebSignup(t *testing.T) {
	smtp := startFakeSMTP(t)
	inst := startInstanceWith(t, fmt.Sprintf(
		"[web]\nmode = \"accounts\"\n[registration]\nmode = \"invite\"\n[mail]\nsmtp_host = %q\nfrom = \"noreply@gitbay.test\"\n",
		smtp.addr))

	// The landing page advertises signup; the form renders.
	status, body := inst.get(t, "/")
	if status != 200 || !strings.Contains(body, `href="/register"`) {
		t.Fatalf("landing signup link: %d", status)
	}
	status, body = inst.get(t, "/register")
	if status != 200 || !strings.Contains(body, "invite-only") || !strings.Contains(body, `name="key"`) {
		t.Fatalf("register form: %d\n%s", status, body)
	}
	// The login page tells a brand-new visitor how to get an account.
	status, body = inst.get(t, "/login")
	if status != 200 || !strings.Contains(body, `href="/register"`) || !strings.Contains(body, "invite-only") {
		t.Fatalf("login signup hint: %d\n%s", status, body)
	}

	// Invite issued over the admin path; redeemed through the browser.
	inst.admin(t, "admin", "invite", "--email", "erin@example.test")
	inviteCode := extractCode(t, smtp.waitMail(t, 0))
	key := inst.newKey(t, "erin")
	pub, err := os.ReadFile(key + ".pub")
	if err != nil {
		t.Fatal(err)
	}

	browser := newBrowser(t)
	// A garbage key re-renders the form with the error, keeping the input.
	_, body = browserPost(t, browser, inst.base()+"/register", url.Values{
		"username": {"erin"}, "invite": {inviteCode}, "key": {"not a key"}})
	if !strings.Contains(body, "does not parse as an SSH public key") || !strings.Contains(body, `value="erin"`) {
		t.Fatalf("bad key handling:\n%s", body)
	}
	// A bad invite fails without burning anything.
	_, body = browserPost(t, browser, inst.base()+"/register", url.Values{
		"username": {"erin"}, "invite": {"deadbeef"}, "key": {string(pub)}})
	if !strings.Contains(body, "invalid or already used") {
		t.Fatalf("bad invite:\n%s", body)
	}
	// The real thing: account is active immediately (invite proves the mailbox).
	status, body = browserPost(t, browser, inst.base()+"/register", url.Values{
		"username": {"erin"}, "invite": {inviteCode}, "key": {string(pub)}})
	if status != 200 || !strings.Contains(body, "welcome, erin") {
		t.Fatalf("signup: %d\n%s", status, body)
	}
	out, errOut, code := inst.ssh(t, key, "", "whoami")
	if code != 0 || !strings.Contains(out, "erin") {
		t.Fatalf("ssh after web signup: exit %d, %s%s", code, out, errOut)
	}
	// The invite is burned: reusing it (fresh browser, fresh key) fails.
	key2 := inst.newKey(t, "mallory")
	pub2, _ := os.ReadFile(key2 + ".pub")
	_, body = browserPost(t, newBrowser(t), inst.base()+"/register", url.Values{
		"username": {"mallory"}, "invite": {inviteCode}, "key": {string(pub2)}})
	if !strings.Contains(body, "invalid or already used") {
		t.Fatalf("invite reuse:\n%s", body)
	}
}

func TestWebSignupClosedInstance(t *testing.T) {
	inst := startInstanceWith(t, "[web]\nmode = \"accounts\"\n")
	// Closed registration: no signup route at all, and no landing hint.
	if status, _ := inst.get(t, "/register"); status != 404 {
		t.Fatalf("register on closed instance: %d", status)
	}
	_, body := inst.get(t, "/")
	if strings.Contains(body, `href="/register"`) {
		t.Fatal("closed landing advertises signup")
	}
	_, body = inst.get(t, "/login")
	if strings.Contains(body, `href="/register"`) {
		t.Fatal("closed login page advertises signup")
	}
	if !strings.Contains(body, "not accepting new accounts") {
		t.Fatalf("closed login page says nothing about accounts:\n%s", body)
	}
}
