package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A rebase that leaves the merge request's diff unchanged keeps its
// fresh approvals; a push that changes the diff stales them (#198).
func TestApprovalsSurviveSameDiffRebase(t *testing.T) {
	inst := startInstance(t)
	aliceKey := inst.newKey(t, "alice")
	bobKey := inst.newKey(t, "bob")
	inst.admin(t, "admin", "user", "create", "alice", "--key", aliceKey+".pub")
	inst.admin(t, "admin", "user", "create", "bob", "--key", bobKey+".pub")
	for _, args := range [][]string{
		{"repo", "create", "alice/app"},
		{"repo", "access", "grant", "alice/app", "bob", "write"},
		{"repo", "settings", "require-approvals", "alice/app", "1"},
	} {
		if _, errOut, code := inst.ssh(t, aliceKey, "", args...); code != 0 {
			t.Fatalf("%v: %s", args, errOut)
		}
	}
	env := inst.gitEnv(aliceKey)
	work := t.TempDir()
	mustGit(t, work, env, "clone", "-q", inst.sshURL("alice/app"), "w")
	dir := filepath.Join(work, "w")
	write := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		mustGit(t, dir, env, "add", name)
		mustGit(t, dir, env, "commit", "-q", "-m", name)
	}
	mustGit(t, dir, env, "checkout", "-q", "-b", "main")
	write("a.txt", "a\n")
	mustGit(t, dir, env, "push", "-q", "origin", "main")

	mustGit(t, dir, env, "checkout", "-q", "-b", "feat")
	write("b.txt", "b\n")
	mustGit(t, dir, env, "push", "-q", "origin", "feat")
	if _, errOut, code := inst.ssh(t, aliceKey, "", "mr", "create", "alice/app",
		"--source", "feat", "--target", "main", "--title", "'feat'"); code != 0 {
		t.Fatalf("mr create: %s", errOut)
	}
	if _, errOut, code := inst.ssh(t, bobKey, "", "mr", "review", "alice/app", "1", "--approve"); code != 0 {
		t.Fatalf("approve: %s", errOut)
	}

	// main moves on; the author rebases and force-pushes. Same diff.
	mustGit(t, dir, env, "checkout", "-q", "main")
	write("c.txt", "c\n")
	mustGit(t, dir, env, "push", "-q", "origin", "main")
	mustGit(t, dir, env, "checkout", "-q", "feat")
	mustGit(t, dir, env, "rebase", "-q", "main")
	mustGit(t, dir, env, "push", "-q", "--force", "origin", "feat")
	out, _, _ := inst.ssh(t, aliceKey, "", "mr", "show", "alice/app", "1", "--json")
	if !strings.Contains(out, `"reviewer":"bob","verdict":"approve","stale":false`) {
		t.Fatalf("approval went stale on a same-diff rebase:\n%s", out)
	}

	// A push that changes the diff stales it, and the merge waits.
	write("b.txt", "b2\n")
	mustGit(t, dir, env, "push", "-q", "origin", "feat")
	out, _, _ = inst.ssh(t, aliceKey, "", "mr", "show", "alice/app", "1", "--json")
	if !strings.Contains(out, `"reviewer":"bob","verdict":"approve","stale":true`) {
		t.Fatalf("approval survived a changed diff:\n%s", out)
	}
	if _, errOut, code := inst.ssh(t, aliceKey, "", "mr", "merge", "alice/app", "1"); code != 4 || !strings.Contains(errOut, "fresh approval") {
		t.Fatalf("merge on a stale approval: %d %s", code, errOut)
	}
	if _, errOut, code := inst.ssh(t, bobKey, "", "mr", "review", "alice/app", "1", "--approve"); code != 0 {
		t.Fatalf("second approve: %s", errOut)
	}
	if _, errOut, code := inst.ssh(t, aliceKey, "", "mr", "merge", "alice/app", "1", "--strategy", "ff"); code != 0 {
		t.Fatalf("merge after fresh approval: %s", errOut)
	}
}
