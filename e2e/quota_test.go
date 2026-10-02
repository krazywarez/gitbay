package e2e

import (
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Per-account and per-org caps on repositories and storage, the cap on
// orgs an account creates, the admin overrides, and expiry of accounts
// that never verified.
func TestQuotasAndPendingExpiry(t *testing.T) {
	t.Setenv("GITBAY_REAP_TICK", "500ms")
	smtp := startFakeSMTP(t)
	inst := startInstanceWith(t, fmt.Sprintf(
		"[registration]\nmode = \"open\"\npending_expiry = \"2s\"\n[mail]\nsmtp_host = %q\nfrom = \"noreply@gitbay.test\"\n"+
			"[limits]\nmax_repos_per_user = 2\nmax_bytes_per_user = 300000\n"+
			"max_orgs_per_user = 1\nmax_repos_per_org = 1\n", smtp.addr))
	rootKey := inst.newKey(t, "root")
	aliceKey := inst.newKey(t, "alice")
	inst.admin(t, "admin", "user", "create", "root", "--key", rootKey+".pub", "--admin")
	inst.admin(t, "admin", "user", "create", "alice", "--key", aliceKey+".pub")

	// Two repositories fit; the third is refused with the numbers.
	for _, r := range []string{"alice/one", "alice/two"} {
		if _, errOut, code := inst.ssh(t, aliceKey, "", "repo", "create", r); code != 0 {
			t.Fatalf("create %s: %s", r, errOut)
		}
	}
	if _, errOut, code := inst.ssh(t, aliceKey, "", "repo", "create", "alice/three"); code != 4 || !strings.Contains(errOut, "2 of the 2 repositories") {
		t.Fatalf("third repo: exit %d %s", code, errOut)
	}
	if _, _, code := inst.ssh(t, aliceKey, "", "repo", "fork", "alice/one", "--name", "onefork"); code != 4 {
		t.Fatal("fork slipped past the cap")
	}
	// One org fits; the second is refused.
	if _, _, code := inst.ssh(t, aliceKey, "", "org", "create", "acme"); code != 0 {
		t.Fatal("org create failed")
	}
	if _, errOut, code := inst.ssh(t, aliceKey, "", "org", "create", "acme2"); code != 4 || !strings.Contains(errOut, "1 of the 1 organizations") {
		t.Fatalf("second org: exit %d %s", code, errOut)
	}
	// The org has its own repository cap, which the admin raises per org.
	if _, errOut, code := inst.ssh(t, aliceKey, "", "repo", "create", "acme/lib"); code != 0 {
		t.Fatalf("org repo: %s", errOut)
	}
	if _, errOut, code := inst.ssh(t, aliceKey, "", "repo", "create", "acme/lib2"); code != 4 || !strings.Contains(errOut, "1 of the 1 repositories") {
		t.Fatalf("second org repo: exit %d %s", code, errOut)
	}
	if out, _, code := inst.ssh(t, rootKey, "", "admin", "org", "limits", "acme", "--repos", "2"); code != 0 || !strings.Contains(out, "repos 1 of 2") {
		t.Fatalf("org limits: exit %d %s", code, out)
	}
	if _, errOut, code := inst.ssh(t, aliceKey, "", "repo", "create", "acme/lib2"); code != 0 {
		t.Fatalf("second org repo after raise: %s", errOut)
	}
	if out, _, code := inst.ssh(t, rootKey, "", "admin", "user", "limits", "alice", "--orgs", "2"); code != 0 || !strings.Contains(out, "orgs 1 of 2") {
		t.Fatalf("user org limit: exit %d %s", code, out)
	}
	if _, errOut, code := inst.ssh(t, aliceKey, "", "org", "create", "acme2"); code != 0 {
		t.Fatalf("second org after raise: %s", errOut)
	}
	// The admin raises the cap for this account; the third fits.
	if out, _, code := inst.ssh(t, rootKey, "", "admin", "user", "limits", "alice", "--repos", "3"); code != 0 || !strings.Contains(out, "repos 2 of 3") {
		t.Fatalf("limits: exit %d %s", code, out)
	}
	if _, errOut, code := inst.ssh(t, aliceKey, "", "repo", "create", "alice/three"); code != 0 {
		t.Fatalf("third repo after raise: %s", errOut)
	}
	if out, _, _ := inst.ssh(t, rootKey, "", "admin", "user", "show", "alice", "--json"); !strings.Contains(out, `"repo_limit":3`) || !strings.Contains(out, `"byte_limit":300000`) {
		t.Fatalf("show lacks limits:\n%s", out)
	}
	if out, _, _ := inst.ssh(t, rootKey, "", "admin", "user", "limits", "alice", "--repos", "default"); !strings.Contains(out, "of 2") {
		t.Fatalf("limits back to default:\n%s", out)
	}
	// A transfer in counts as a create for the receiving owner.
	if _, errOut, code := inst.ssh(t, aliceKey, "", "repo", "transfer", "acme/lib2", "alice"); code != 4 || !strings.Contains(errOut, "3 of the 2 repositories") {
		t.Fatalf("transfer past the cap: exit %d %s", code, errOut)
	}
	if _, errOut, code := inst.ssh(t, aliceKey, "", "repo", "transfer", "alice/three", "acme2"); code != 0 {
		t.Fatalf("transfer into an org with room: %s", errOut)
	}

	// Storage: a push past what the account has left is refused.
	work := t.TempDir()
	env := inst.gitEnv(aliceKey)
	mustGit(t, work, env, "clone", inst.sshURL("alice/one"), "w")
	dir := filepath.Join(work, "w")
	os.WriteFile(filepath.Join(dir, "small.txt"), []byte("ok\n"), 0o644)
	mustGit(t, dir, env, "checkout", "-q", "-b", "main")
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, env, "commit", "-q", "-m", "small")
	mustGit(t, dir, env, "push", "-q", "origin", "main")
	big := make([]byte, 400_000)
	rand.Read(big)
	os.WriteFile(filepath.Join(dir, "big.bin"), big, 0o644)
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, env, "commit", "-q", "-m", "big")
	if out, code := gitRun(t, dir, env, "push", "origin", "main"); code == 0 || !strings.Contains(out, "max") {
		t.Fatalf("push past the storage cap accepted: exit %d\n%s", code, out)
	}
	if _, _, code := inst.ssh(t, rootKey, "", "admin", "user", "limits", "alice", "--bytes", "0"); code != 0 {
		t.Fatal("lift byte cap failed")
	}
	mustGit(t, dir, env, "push", "-q", "origin", "main")

	// An account that registers and never verifies is removed after
	// pending_expiry; the name is free again.
	newKey := inst.newKey(t, "dana")
	if _, errOut, code := inst.ssh(t, newKey, "", "register", "--username", "dana", "--email", "dana@example.test"); code != 0 {
		t.Fatalf("register: %s", errOut)
	}
	if out, _, _ := inst.ssh(t, rootKey, "", "admin", "user", "list", "--state", "pending"); !strings.HasPrefix(out, "dana\t") {
		t.Fatalf("dana not pending:\n%s", out)
	}
	deadline := time.Now().Add(15 * time.Second)
	for {
		out, _, _ := inst.ssh(t, rootKey, "", "admin", "user", "list", "--state", "pending")
		if strings.TrimSpace(out) == "" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("pending account never expired:\n%s", out)
		}
		time.Sleep(300 * time.Millisecond)
	}
	if _, _, code := inst.ssh(t, newKey, "", "whoami"); code == 0 {
		t.Fatal("expired account still authenticates")
	}
	if out := inst.admin(t, "admin", "audit", "--action", "pending.expired"); !strings.Contains(out, `"user":"dana"`) {
		t.Fatalf("expiry not audited:\n%s", out)
	}
}
