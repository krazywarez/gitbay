package e2e

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// mailTo returns captured messages addressed to one recipient.
func (f *fakeSMTP) mailTo(recipient string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, m := range f.mail {
		if strings.Contains(m, "To: "+recipient) {
			out = append(out, m)
		}
	}
	return out
}

func (f *fakeSMTP) waitFor(t *testing.T, recipient, substr string) string {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		for _, m := range f.mailTo(recipient) {
			if strings.Contains(m, substr) {
				return m
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("no mail to %s containing %q", recipient, substr)
	return ""
}

func TestActivityNotifications(t *testing.T) {
	smtp := startFakeSMTP(t)
	inst := startInstanceWith(t, fmt.Sprintf(
		"[mail]\nsmtp_host = %q\nfrom = \"noreply@gitbay.test\"\n", smtp.addr))
	// Fast retries for the mailer loop.
	inst.proc.Process.Kill()
	inst.proc.Wait()
	inst.proc = exec.Command(inst.gitbayd, "--config", inst.config, "serve")
	inst.proc.Env = append(os.Environ(), "GITBAY_WEBHOOK_RETRY_BASE=500ms")
	inst.proc.Stderr = os.Stderr
	if err := inst.proc.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { inst.proc.Process.Kill(); inst.proc.Wait() })
	deadline := time.Now().Add(10 * time.Second)
	for {
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", inst.port), 200*time.Millisecond)
		if err == nil {
			conn.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("daemon did not restart")
		}
		time.Sleep(50 * time.Millisecond)
	}

	aliceKey := inst.newKey(t, "alice")
	bobKey := inst.newKey(t, "bob")
	eveKey := inst.newKey(t, "eve")
	inst.admin(t, "admin", "user", "create", "alice",
		"--key", aliceKey+".pub", "--email", "alice@example.test", "--verified")
	inst.admin(t, "admin", "user", "create", "bob",
		"--key", bobKey+".pub", "--email", "bob@example.test", "--verified")
	// eve has NO verified email: she must be skipped silently.
	inst.admin(t, "admin", "user", "create", "eve",
		"--key", eveKey+".pub", "--email", "eve@example.test")

	if _, errOut, code := inst.ssh(t, aliceKey, "", "repo", "create", "alice/app"); code != 0 {
		t.Fatalf("repo create: %s", errOut)
	}

	// Bob opens an issue: the repo owner (alice) is notified with subject,
	// excerpt, and link; bob (the actor) is not.
	if _, errOut, code := inst.ssh(t, bobKey, "",
		"issue", "create", "alice/app", "--title", "'it leaks'", "--body", "'memory climbs forever'"); code != 0 {
		t.Fatalf("issue create: %s", errOut)
	}
	m := smtp.waitFor(t, "alice@example.test", "#1")
	if !strings.Contains(m, "[alice/app] #1: it leaks") ||
		!strings.Contains(m, "bob opened issue #1") ||
		!strings.Contains(m, "memory climbs forever") ||
		!strings.Contains(m, "/alice/app/issues/1") {
		t.Fatalf("issue-open mail:\n%s", m)
	}
	if n := len(smtp.mailTo("bob@example.test")); n != 0 {
		t.Fatalf("actor notified about own action (%d mails)", n)
	}

	// Alice comments: bob (author/participant) is notified; alice is not.
	if _, _, code := inst.ssh(t, aliceKey, "", "issue", "comment", "alice/app", "1", "--message", "'on it'"); code != 0 {
		t.Fatal("comment failed")
	}
	m = smtp.waitFor(t, "bob@example.test", "commented")
	if !strings.Contains(m, "alice commented on #1") || !strings.Contains(m, "on it") {
		t.Fatalf("comment mail:\n%s", m)
	}

	// Eve comments (unverified email, still allowed to act): both alice
	// and bob get mail; eve never receives any.
	if _, _, code := inst.ssh(t, eveKey, "", "issue", "comment", "alice/app", "1", "--message", "'same here'"); code != 0 {
		t.Fatal("eve comment failed")
	}
	smtp.waitFor(t, "alice@example.test", "same here")
	smtp.waitFor(t, "bob@example.test", "same here")

	// Close notifies participants.
	if _, _, code := inst.ssh(t, aliceKey, "", "issue", "close", "alice/app", "1"); code != 0 {
		t.Fatal("close failed")
	}
	m = smtp.waitFor(t, "bob@example.test", "closed #1")
	if !strings.Contains(m, "alice closed #1") {
		t.Fatalf("close mail:\n%s", m)
	}
	if n := len(smtp.mailTo("eve@example.test")); n != 0 {
		t.Fatalf("unverified recipient got mail (%d)", n)
	}

	// MR flow: bob opens (alice notified), alice reviews (bob notified),
	// alice merges (bob notified).
	if _, _, code := inst.ssh(t, aliceKey, "", "repo", "access", "grant", "alice/app", "bob", "write"); code != 0 {
		t.Fatal("grant failed")
	}
	work := t.TempDir()
	bobEnv := inst.gitEnv(bobKey)
	mustGit(t, work, bobEnv, "clone", inst.sshURL("alice/app"), "w")
	dir := work + "/w"
	os.WriteFile(dir+"/f.txt", []byte("x\n"), 0o644)
	mustGit(t, dir, bobEnv, "checkout", "-q", "-b", "main")
	mustGit(t, dir, bobEnv, "add", ".")
	mustGit(t, dir, bobEnv, "commit", "-q", "-m", "base")
	mustGit(t, dir, bobEnv, "push", "-q", "origin", "main")
	mustGit(t, dir, bobEnv, "checkout", "-q", "-b", "feat")
	mustGit(t, dir, bobEnv, "commit", "-q", "--allow-empty", "-m", "work")
	mustGit(t, dir, bobEnv, "push", "-q", "origin", "feat")
	if _, errOut, code := inst.ssh(t, bobKey, "", "mr", "create", "alice/app",
		"--source", "feat", "--target", "main", "--title", "'ship it'"); code != 0 {
		t.Fatalf("mr create: %s", errOut)
	}
	m = smtp.waitFor(t, "alice@example.test", "!1")
	if !strings.Contains(m, "bob opened merge request !1") {
		t.Fatalf("mr-open mail:\n%s", m)
	}
	if _, _, code := inst.ssh(t, aliceKey, "", "mr", "review", "alice/app", "1", "--approve"); code != 0 {
		t.Fatal("review failed")
	}
	smtp.waitFor(t, "bob@example.test", "reviewed !1: approve")
	if _, errOut, code := inst.ssh(t, aliceKey, "", "mr", "merge", "alice/app", "1"); code != 0 {
		t.Fatalf("merge: %s", errOut)
	}
	m = smtp.waitFor(t, "bob@example.test", "merged !1")
	if !strings.Contains(m, "alice merged !1 into main") {
		t.Fatalf("merge mail:\n%s", m)
	}
}
