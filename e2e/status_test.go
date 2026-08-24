package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCommitStatuses(t *testing.T) {
	inst := startInstance(t)
	aliceKey := inst.newKey(t, "alice")
	bobKey := inst.newKey(t, "bob")
	eveKey := inst.newKey(t, "eve")
	inst.admin(t, "admin", "user", "create", "alice",
		"--key", aliceKey+".pub", "--email", "alice@example.test", "--verified")
	inst.admin(t, "admin", "user", "create", "bob", "--key", bobKey+".pub")
	inst.admin(t, "admin", "user", "create", "eve", "--key", eveKey+".pub")

	// Repo with a branch and an MR.
	if _, errOut, code := inst.ssh(t, aliceKey, "", "repo", "create", "alice/svc"); code != 0 {
		t.Fatalf("repo create: %s", errOut)
	}
	if _, _, code := inst.ssh(t, aliceKey, "", "repo", "access", "grant", "alice/svc", "bob", "write"); code != 0 {
		t.Fatal("grant failed")
	}
	work := t.TempDir()
	env := inst.gitEnv(aliceKey)
	mustGit(t, work, env, "clone", inst.sshURL("alice/svc"), "w")
	dir := filepath.Join(work, "w")
	os.WriteFile(filepath.Join(dir, "svc.txt"), []byte("v1\n"), 0o644)
	mustGit(t, dir, env, "checkout", "-q", "-b", "main")
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, env, "commit", "-q", "-m", "base")
	mustGit(t, dir, env, "push", "-q", "origin", "main")
	mustGit(t, dir, env, "checkout", "-q", "-b", "feat")
	mustGit(t, dir, env, "commit", "-q", "--allow-empty", "-m", "feature")
	mustGit(t, dir, env, "push", "-q", "origin", "feat")
	head := strings.TrimSpace(mustGit(t, dir, env, "rev-parse", "feat"))
	if _, errOut, code := inst.ssh(t, aliceKey, "", "mr", "create", "alice/svc",
		"--source", "feat", "--target", "main", "--title", "'gated'"); code != 0 {
		t.Fatalf("mr create: %s", errOut)
	}

	// Reporting requires write: eve (read-only public) is denied.
	if _, _, code := inst.ssh(t, eveKey, "", "status", "set", "alice/svc", head, "--context", "build", "--state", "success"); code != 4 {
		t.Fatal("reader reported a status")
	}

	// Bob (write) reports pending, then success: upsert, not duplicate.
	if _, errOut, code := inst.ssh(t, bobKey, "", "status", "set", "alice/svc", head,
		"--context", "build", "--state", "pending", "--description", "'compiling'"); code != 0 {
		t.Fatalf("status set: %s", errOut)
	}
	if _, _, code := inst.ssh(t, bobKey, "", "status", "set", "alice/svc", head,
		"--context", "build", "--state", "success", "--url", "https://ci.example/1"); code != 0 {
		t.Fatal("status update failed")
	}
	out, _, _ := inst.ssh(t, aliceKey, "", "status", "list", "alice/svc", head, "--json")
	if !strings.Contains(out, `"combined":"success"`) || strings.Count(out, `"context":"build"`) != 1 {
		t.Fatalf("status list after upsert: %s", out)
	}

	// A second failing context drags the combined state down; mr show sees it.
	if _, _, code := inst.ssh(t, bobKey, "", "status", "set", "alice/svc", head,
		"--context", "lint", "--state", "failure"); code != 0 {
		t.Fatal("lint status failed")
	}
	out, _, _ = inst.ssh(t, aliceKey, "", "mr", "show", "alice/svc", "1", "--json")
	if !strings.Contains(out, `"checks_combined":"failure"`) {
		t.Fatalf("mr show checks: %s", out)
	}

	// require-checks gates the merge: failing -> refused naming context,
	// green -> merges.
	if _, _, code := inst.ssh(t, aliceKey, "", "repo", "settings", "require-checks", "alice/svc", "on"); code != 0 {
		t.Fatal("require-checks failed")
	}
	_, errOut, code := inst.ssh(t, aliceKey, "", "mr", "merge", "alice/svc", "1")
	if code != 4 || !strings.Contains(errOut, "lint=failure") {
		t.Fatalf("merge with red checks: exit %d, %s", code, errOut)
	}
	if _, _, code := inst.ssh(t, bobKey, "", "status", "set", "alice/svc", head, "--context", "lint", "--state", "success"); code != 0 {
		t.Fatal("lint fix failed")
	}
	if _, errOut, code = inst.ssh(t, aliceKey, "", "mr", "merge", "alice/svc", "1"); code != 0 {
		t.Fatalf("merge with green checks: %s", errOut)
	}

	// A repo requiring checks refuses merges with NO statuses at all.
	mustGit(t, dir, env, "checkout", "-q", "-b", "feat2", "main")
	mustGit(t, dir, env, "commit", "-q", "--allow-empty", "-m", "next")
	mustGit(t, dir, env, "push", "-q", "origin", "feat2")
	if _, _, code := inst.ssh(t, aliceKey, "", "mr", "create", "alice/svc",
		"--source", "feat2", "--target", "main", "--title", "'unchecked'"); code != 0 {
		t.Fatal("mr2 create failed")
	}
	_, errOut, code = inst.ssh(t, aliceKey, "", "mr", "merge", "alice/svc", "2")
	if code != 4 || !strings.Contains(errOut, "none were reported") {
		t.Fatalf("merge with no checks: exit %d, %s", code, errOut)
	}

	// Web: the commit page shows check badges.
	status, body := inst.get(t, "/alice/svc/commit/"+head)
	if status != 200 || !strings.Contains(body, "check-success") || !strings.Contains(body, "build") {
		t.Fatalf("commit page checks: %d", status)
	}
}
