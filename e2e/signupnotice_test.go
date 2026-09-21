package e2e

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// registration.notify_admin mails the instance's admins when an account
// becomes active. An open-mode signup counts at verification, not when
// the row is created, so an unverified attempt is silent (#234).
func TestSignupNotifiesAdmins(t *testing.T) {
	t.Parallel()
	smtp := startFakeSMTP(t)
	inst := startInstanceWith(t, fmt.Sprintf(
		"[registration]\nmode = \"open\"\nnotify_admin = true\n"+
			"[mail]\nsmtp_host = %q\nfrom = \"noreply@gitbay.test\"\n", smtp.addr))
	rootKey := inst.newKey(t, "root")
	inst.admin(t, "admin", "user", "create", "root", "--key", rootKey+".pub",
		"--admin", "--email", "root@example.test", "--verified")
	// A second admin with no verified address is skipped, not an error.
	inst.admin(t, "admin", "user", "create", "quiet", "--key", inst.newKey(t, "quiet")+".pub", "--admin")

	danaKey := inst.newKey(t, "dana")
	if _, errOut, code := inst.ssh(t, danaKey, "", "register",
		"--username", "dana", "--email", "dana@example.test"); code != 0 {
		t.Fatalf("register: %s", errOut)
	}
	// Pending: the admin has heard nothing yet.
	time.Sleep(5 * time.Second) // the mailer ticks every two seconds
	if got := smtp.mailTo("root@example.test"); len(got) != 0 {
		t.Fatalf("admin mailed before the account was verified:\n%s", got[0])
	}

	code := extractCode(t, smtp.waitFor(t, "dana@example.test", "email verify"))
	if _, errOut, ec := inst.ssh(t, danaKey, "", "email", "verify", code); ec != 0 {
		t.Fatalf("verify: %s", errOut)
	}
	msg := smtp.waitFor(t, "root@example.test", "new account")
	if !strings.Contains(msg, "dana") || !strings.Contains(msg, "open registration") {
		t.Fatalf("notice body:\n%s", msg)
	}
}

// With notify_admin off, the default, the same signup mails no one but
// the person registering.
func TestSignupNoticeOffByDefault(t *testing.T) {
	t.Parallel()
	smtp := startFakeSMTP(t)
	inst := startInstanceWith(t, fmt.Sprintf(
		"[registration]\nmode = \"open\"\n[mail]\nsmtp_host = %q\nfrom = \"noreply@gitbay.test\"\n", smtp.addr))
	rootKey := inst.newKey(t, "root")
	inst.admin(t, "admin", "user", "create", "root", "--key", rootKey+".pub",
		"--admin", "--email", "root@example.test", "--verified")

	eveKey := inst.newKey(t, "eve")
	if _, errOut, ec := inst.ssh(t, eveKey, "", "register",
		"--username", "eve", "--email", "eve@example.test"); ec != 0 {
		t.Fatalf("register: %s", errOut)
	}
	code := extractCode(t, smtp.waitFor(t, "eve@example.test", "email verify"))
	if _, errOut, ec := inst.ssh(t, eveKey, "", "email", "verify", code); ec != 0 {
		t.Fatalf("verify: %s", errOut)
	}
	time.Sleep(5 * time.Second)
	if got := smtp.mailTo("root@example.test"); len(got) != 0 {
		t.Fatalf("admin mailed with notify_admin off:\n%s", got[0])
	}
}
