package e2e

import (
	"bufio"
	"fmt"
	"net"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeSMTP is a minimal SMTP server capturing delivered messages.
type fakeSMTP struct {
	addr string
	mu   sync.Mutex
	mail []string // raw DATA payloads
}

func startFakeSMTP(t *testing.T) *fakeSMTP {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	f := &fakeSMTP{addr: ln.Addr().String()}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go f.handle(conn)
		}
	}()
	return f
}

func (f *fakeSMTP) handle(conn net.Conn) {
	defer conn.Close()
	r := bufio.NewReader(conn)
	say := func(s string) { fmt.Fprintf(conn, "%s\r\n", s) }
	say("220 fake ESMTP")
	var data strings.Builder
	inData := false
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		if inData {
			if line == "." {
				f.mu.Lock()
				f.mail = append(f.mail, data.String())
				f.mu.Unlock()
				data.Reset()
				inData = false
				say("250 ok")
				continue
			}
			data.WriteString(line + "\n")
			continue
		}
		switch {
		case strings.HasPrefix(line, "EHLO"), strings.HasPrefix(line, "HELO"):
			fmt.Fprintf(conn, "250-fake\r\n250 SIZE 1000000\r\n")
		case strings.HasPrefix(line, "MAIL"), strings.HasPrefix(line, "RCPT"):
			say("250 ok")
		case line == "DATA":
			inData = true
			say("354 go")
		case line == "QUIT":
			say("221 bye")
			return
		default:
			say("250 ok")
		}
	}
}

// waitMail returns the nth captured message.
func (f *fakeSMTP) waitMail(t *testing.T, n int) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		f.mu.Lock()
		if len(f.mail) > n {
			m := f.mail[n]
			f.mu.Unlock()
			return m
		}
		f.mu.Unlock()
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("mail %d never arrived", n)
	return ""
}

var codePat = regexp.MustCompile(`(?:verify|--invite) ([0-9a-f]{64})`)

func extractCode(t *testing.T, mail string) string {
	t.Helper()
	m := codePat.FindStringSubmatch(mail)
	if m == nil {
		t.Fatalf("no code in mail:\n%s", mail)
	}
	return m[1]
}

func TestOpenRegistration(t *testing.T) {
	smtp := startFakeSMTP(t)
	inst := startInstanceWith(t, fmt.Sprintf(
		"[registration]\nmode = \"open\"\n[mail]\nsmtp_host = %q\nfrom = \"noreply@gitbay.test\"\n", smtp.addr))

	// A stranger's key cannot run normal commands, and the denial explains
	// how to register.
	newKey := inst.newKey(t, "newcomer")
	_, errOut, code := inst.ssh(t, newKey, "", "whoami")
	if code != 4 || !strings.Contains(errOut, "register --username") {
		t.Fatalf("stranger whoami: exit %d, %s", code, errOut)
	}

	// Register: account created pending, verification mail sent.
	out, errOut, code := inst.ssh(t, newKey, "", "register", "--username", "dana", "--email", "dana@example.test")
	if code != 0 {
		t.Fatalf("register: exit %d, %s", code, errOut)
	}
	if !strings.Contains(out, "verification code was sent") {
		t.Fatalf("register output: %s", out)
	}
	msg := smtp.waitMail(t, 0)
	if !strings.Contains(msg, "To: dana@example.test") || !strings.Contains(msg, "From: noreply@gitbay.test") {
		t.Fatalf("mail headers:\n%s", msg)
	}

	// Pending: the key authenticates, whoami works, but everything else is
	// gated — control commands and git alike.
	if out, _, code = inst.ssh(t, newKey, "", "whoami"); code != 0 || strings.TrimSpace(out) != "dana" {
		t.Fatalf("pending whoami: %d %q", code, out)
	}
	_, errOut, code = inst.ssh(t, newKey, "", "repo", "create", "dana/proj")
	if code != 4 || !strings.Contains(errOut, "not active yet") {
		t.Fatalf("pending repo create: exit %d, %s", code, errOut)
	}
	cloneOut, cloneCode := gitRun(t, t.TempDir(), inst.gitEnv(newKey), "clone", inst.sshURL("dana/anything"))
	if cloneCode == 0 || !strings.Contains(cloneOut, "not active yet") {
		t.Fatalf("pending git: %d\n%s", cloneCode, cloneOut)
	}

	// A wrong code fails; the mailed code activates the account.
	if _, _, code = inst.ssh(t, newKey, "", "email", "verify", strings.Repeat("0", 64)); code != 2 {
		t.Fatalf("bad code: exit %d, want 2", code)
	}
	verifyCode := extractCode(t, msg)
	out, errOut, code = inst.ssh(t, newKey, "", "email", "verify", verifyCode)
	if code != 0 || !strings.Contains(out, "account is active") {
		t.Fatalf("verify: exit %d, %s%s", code, out, errOut)
	}
	// Single use.
	if _, _, code = inst.ssh(t, newKey, "", "email", "verify", verifyCode); code != 2 {
		t.Fatalf("code reuse: exit %d, want 2", code)
	}

	// Fully active: repo create works, and the verified email makes
	// signature verification meaningful (verified_by = smtp).
	if _, errOut, code = inst.ssh(t, newKey, "", "repo", "create", "dana/proj"); code != 0 {
		t.Fatalf("post-verify repo create: %s", errOut)
	}

	// Self-service email add on an existing account sends a second mail.
	if _, errOut, code = inst.ssh(t, newKey, "", "email", "add", "dana2@example.test"); code != 0 {
		t.Fatalf("email add: %s", errOut)
	}
	msg2 := smtp.waitMail(t, 1)
	if !strings.Contains(msg2, "To: dana2@example.test") {
		t.Fatalf("second mail:\n%s", msg2)
	}
	if _, _, code = inst.ssh(t, newKey, "", "email", "verify", extractCode(t, msg2)); code != 0 {
		t.Fatal("second verify failed")
	}
}

func TestInviteRegistration(t *testing.T) {
	smtp := startFakeSMTP(t)
	inst := startInstanceWith(t, fmt.Sprintf(
		"[registration]\nmode = \"invite\"\n[mail]\nsmtp_host = %q\nfrom = \"noreply@gitbay.test\"\n", smtp.addr))

	// Registering without an invite is refused.
	newKey := inst.newKey(t, "guest")
	_, errOut, code := inst.ssh(t, newKey, "", "register", "--username", "erin", "--email", "erin@example.test")
	if code != 4 || !strings.Contains(errOut, "invite-only") {
		t.Fatalf("uninvited register: exit %d, %s", code, errOut)
	}

	// Admin issues an invite; the code arrives by mail.
	out := inst.admin(t, "admin", "invite", "--email", "erin@example.test")
	if !strings.Contains(out, "invite emailed") {
		t.Fatalf("invite output: %s", out)
	}
	inviteCode := extractCode(t, smtp.waitMail(t, 0))

	// Redeeming it creates an ACTIVE account: code possession proves the
	// mailbox, so the email is verified (by smtp) and nothing is pending.
	out, errOut, code = inst.ssh(t, newKey, "", "register", "--username", "erin", "--invite", inviteCode)
	if code != 0 || !strings.Contains(out, "account is active") {
		t.Fatalf("invite register: exit %d, %s%s", code, out, errOut)
	}
	if _, errOut, code = inst.ssh(t, newKey, "", "repo", "create", "erin/proj"); code != 0 {
		t.Fatalf("invited user repo create: %s", errOut)
	}

	// Invites are single-use.
	otherKey := inst.newKey(t, "other")
	_, errOut, code = inst.ssh(t, otherKey, "", "register", "--username", "fake", "--invite", inviteCode)
	if code != 4 || !strings.Contains(errOut, "already used") {
		t.Fatalf("invite reuse: exit %d, %s", code, errOut)
	}
}
