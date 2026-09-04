package e2e

import (
	"crypto/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAuditAndHardening(t *testing.T) {
	inst := startInstanceWith(t, "[limits]\nssh_auth_rate = 3\nmax_pack_bytes = 2000\n")
	adminKey := inst.newKey(t, "root")
	aliceKey := inst.newKey(t, "alice")
	bobKey := inst.newKey(t, "bob")
	inst.admin(t, "admin", "user", "create", "root", "--key", adminKey+".pub", "--admin")
	inst.admin(t, "admin", "user", "create", "alice", "--key", aliceKey+".pub")
	inst.admin(t, "admin", "user", "create", "bob", "--key", bobKey+".pub")

	// Mutating commands land in the audit log with source fingerprints;
	// reads do not. Admin-only over SSH; host admin command works too.
	if _, _, code := inst.ssh(t, aliceKey, "", "repo", "create", "alice/app"); code != 0 {
		t.Fatal("repo create failed")
	}
	if _, _, code := inst.ssh(t, aliceKey, "", "repo", "access", "grant", "alice/app", "bob", "write"); code != 0 {
		t.Fatal("grant failed")
	}
	if _, _, code := inst.ssh(t, aliceKey, "", "repo", "list"); code != 0 {
		t.Fatal("repo list failed")
	}
	if _, _, code := inst.ssh(t, aliceKey, "", "audit"); code != 4 {
		t.Fatal("non-admin read the audit log")
	}
	out, _, code := inst.ssh(t, adminKey, "", "audit", "--json")
	if code != 0 || !strings.Contains(out, "cmd repo create") ||
		!strings.Contains(out, "cmd repo access grant") ||
		!strings.Contains(out, `SHA256:`) || // key fingerprint as source
		!strings.Contains(out, "admin user.created") {
		t.Fatalf("audit content: %s", out)
	}
	if strings.Contains(out, "cmd repo list") {
		t.Fatal("read-only command audited")
	}
	if out := inst.admin(t, "admin", "audit", "--limit", "5"); !strings.Contains(out, "cmd repo") {
		t.Fatalf("host audit: %s", out)
	}

	// Prose reaches argv through --title and --body. The entry records
	// that the flags were given, not what was written: the issue itself is
	// the record of its own text, and the audit log is not pruned by
	// default (#122).
	if _, errOut, code := inst.ssh(t, aliceKey, "", "issue", "create", "alice/app",
		"--title", "'a short title'", "--body", "'prose that must not be copied'"); code != 0 {
		t.Fatalf("issue create: %s", errOut)
	}
	out, _, code = inst.ssh(t, adminKey, "", "audit", "--json")
	if code != 0 || !strings.Contains(out, "cmd issue create") {
		t.Fatalf("issue create not audited: %s", out)
	}
	if strings.Contains(out, "prose that must not be copied") || strings.Contains(out, "a short title") {
		t.Fatalf("audit log copied the issue text:\n%s", out)
	}
	if !strings.Contains(out, "--body") || !strings.Contains(out, "alice/app") {
		t.Fatalf("audit log dropped the flag names or the target:\n%s", out)
	}

	// Disable: everything refused, sessions dropped, nothing deleted.
	inst.admin(t, "admin", "user", "disable", "bob")
	if _, errOut, code := inst.ssh(t, bobKey, "", "whoami"); code != 4 || !strings.Contains(errOut, "disabled") {
		t.Fatalf("disabled ssh: exit %d, %s", code, errOut)
	}
	inst.admin(t, "admin", "user", "enable", "bob")
	if _, _, code := inst.ssh(t, bobKey, "", "whoami"); code != 0 {
		t.Fatal("re-enabled user still refused")
	}

	// max_pack_bytes: an oversized push is refused by receive-pack.
	work := t.TempDir()
	env := inst.gitEnv(aliceKey)
	mustGit(t, work, env, "clone", inst.sshURL("alice/app"), "w")
	dir := filepath.Join(work, "w")
	big := make([]byte, 200_000)
	rand.Read(big) // incompressible: the pack must exceed max_pack_bytes
	os.WriteFile(filepath.Join(dir, "big.bin"), big, 0o644)
	mustGit(t, dir, env, "checkout", "-q", "-b", "main")
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, env, "commit", "-q", "-m", "big")
	if out, code := gitRun(t, dir, env, "push", "origin", "main"); code == 0 || !strings.Contains(out, "max") {
		t.Fatalf("oversized push accepted: exit %d\n%s", code, out)
	}
	// A normal-sized push still works.
	mustGit(t, dir, env, "rm", "-q", "big.bin")
	os.WriteFile(filepath.Join(dir, "small.txt"), []byte("ok\n"), 0o644)
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, env, "commit", "-q", "--amend", "-m", "small")
	mustGit(t, dir, env, "push", "-q", "origin", "main")

	// Auth rate limit, LAST because it locks out this whole IP: a burst
	// of unknown-key failures throttles further auth — even a valid key
	// — until the window passes. (Registration is closed, so unknown
	// keys fail auth.) The audit is read host-locally: SSH is locked.
	strangerKey := inst.newKey(t, "stranger")
	for i := 0; i < 5; i++ {
		inst.ssh(t, strangerKey, "", "whoami")
	}
	if _, _, code := inst.ssh(t, adminKey, "", "whoami"); code == 0 {
		t.Fatal("valid key not throttled after failure burst")
	}
	auditOut := inst.admin(t, "admin", "audit")
	if !strings.Contains(auditOut, "auth.failed") || !strings.Contains(auditOut, "auth.throttled") {
		t.Fatalf("burst not audited:\n%s", auditOut)
	}
	if strings.Count(auditOut, "auth.throttled") != 1 {
		t.Fatal("throttle audited more than once per window")
	}
}
