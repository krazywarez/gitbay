package e2e

import (
	"fmt"
	"net/url"
	"strings"
	"testing"
)

// A person with no SSH key can still get into the web UI: they ask for a
// link by username or verified address and it arrives by mail (#155).
func TestEmailLogin(t *testing.T) {
	smtp := startFakeSMTP(t)
	inst := startInstanceWith(t, fmt.Sprintf(
		"[web]\nmode = \"accounts\"\n[mail]\nsmtp_host = %q\nfrom = \"noreply@gitbay.test\"\n",
		smtp.addr))

	// No --key: this account has no way to authenticate over SSH at all,
	// which is the whole point.
	inst.admin(t, "admin", "user", "create", "dana",
		"--email", "dana@example.test", "--verified")

	browser := newBrowser(t)
	status, body := browserPost(t, browser, inst.base()+"/login",
		url.Values{"identifier": {"dana@example.test"}})
	if status != 200 {
		t.Fatalf("POST /login: %d", status)
	}
	if !strings.Contains(body, "on its way") {
		t.Fatalf("no confirmation in body: %s", body)
	}

	msg := smtp.waitFor(t, "dana@example.test", "/login?token=")
	i := strings.Index(msg, "/login?token=")
	link := msg[i:]
	if j := strings.IndexAny(link, " \r\n"); j >= 0 {
		link = link[:j]
	}

	if status, _ := browserGet(t, browser, inst.base()+link); status != 200 {
		t.Fatalf("following the link: %d", status)
	}
	status, body = browserGet(t, browser, inst.base()+"/settings")
	if status != 200 || !strings.Contains(body, "dana@example.test") {
		t.Fatalf("not logged in after the link: %d", status)
	}

	// The link is single use. The client follows the logged-out redirect to
	// /login, so the page body tells the two apart, not the status (the
	// redirect target is a 200 either way).
	second := newBrowser(t)
	browserGet(t, second, inst.base()+link)
	if _, body := browserGet(t, second, inst.base()+"/settings"); strings.Contains(body, "dana@example.test") {
		t.Error("login link worked twice")
	}
}

// The response must not say whether an account exists. A different status,
// body, or destination answers "is this person here?" to anyone who asks.
func TestEmailLoginDoesNotEnumerate(t *testing.T) {
	smtp := startFakeSMTP(t)
	inst := startInstanceWith(t, fmt.Sprintf(
		"[web]\nmode = \"accounts\"\n[mail]\nsmtp_host = %q\nfrom = \"noreply@gitbay.test\"\n",
		smtp.addr))
	inst.admin(t, "admin", "user", "create", "dana",
		"--email", "dana@example.test", "--verified")
	// An account whose address was never verified must look like an absent
	// one, or an unverified address becomes an oracle.
	inst.admin(t, "admin", "user", "create", "eve", "--email", "eve@example.test")

	browser := newBrowser(t)
	real1, bodyReal := browserPost(t, browser, inst.base()+"/login",
		url.Values{"identifier": {"dana@example.test"}})
	absent, bodyAbsent := browserPost(t, browser, inst.base()+"/login",
		url.Values{"identifier": {"nobody@example.test"}})
	unver, bodyUnver := browserPost(t, browser, inst.base()+"/login",
		url.Values{"identifier": {"eve@example.test"}})
	empty, bodyEmpty := browserPost(t, browser, inst.base()+"/login",
		url.Values{"identifier": {""}})

	for _, c := range []struct {
		name   string
		status int
		body   string
	}{
		{"absent", absent, bodyAbsent},
		{"unverified", unver, bodyUnver},
		{"empty", empty, bodyEmpty},
	} {
		if c.status != real1 || c.body != bodyReal {
			t.Errorf("%s differs from a real address: status %d vs %d", c.name, c.status, real1)
		}
	}
	if len(smtp.mailTo("eve@example.test")) != 0 {
		t.Error("mailed an unverified address")
	}
	if len(smtp.mailTo("nobody@example.test")) != 0 {
		t.Error("mailed an address with no account")
	}
}

// An anonymous endpoint that sends mail needs a durable per-account bound,
// the same one email verification has (#136).
func TestEmailLoginThrottled(t *testing.T) {
	smtp := startFakeSMTP(t)
	inst := startInstanceWith(t, fmt.Sprintf(
		"[web]\nmode = \"accounts\"\n[mail]\nsmtp_host = %q\nfrom = \"noreply@gitbay.test\"\n",
		smtp.addr))
	inst.admin(t, "admin", "user", "create", "dana",
		"--email", "dana@example.test", "--verified")

	browser := newBrowser(t)
	for i := 0; i < 6; i++ {
		browserPost(t, browser, inst.base()+"/login",
			url.Values{"identifier": {"dana@example.test"}})
	}
	if n := len(smtp.mailTo("dana@example.test")); n > 5 {
		t.Fatalf("sent %d login mails in an hour, want at most 5", n)
	}
}
