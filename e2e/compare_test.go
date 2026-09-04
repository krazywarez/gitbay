package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The compare view shows what a branch adds on top of another from their
// merge base, and repo diff is the same range over ssh (#118).
func TestCompareView(t *testing.T) {
	inst := startInstance(t)
	aliceKey := inst.newKey(t, "alice")
	inst.admin(t, "admin", "user", "create", "alice", "--key", aliceKey+".pub")
	if _, errOut, code := inst.ssh(t, aliceKey, "", "repo", "create", "alice/app"); code != 0 {
		t.Fatalf("repo create: %s", errOut)
	}
	work := t.TempDir()
	env := inst.gitEnv(aliceKey)
	mustGit(t, work, env, "clone", inst.sshURL("alice/app"), "w")
	dir := filepath.Join(work, "w")
	os.WriteFile(filepath.Join(dir, "f.txt"), []byte("one\n"), 0o644)
	mustGit(t, dir, env, "checkout", "-q", "-b", "main")
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, env, "commit", "-q", "-m", "base")
	mustGit(t, dir, env, "push", "-q", "origin", "main")
	mustGit(t, dir, env, "checkout", "-q", "-b", "feat")
	os.WriteFile(filepath.Join(dir, "f.txt"), []byte("one\ntwo\n"), 0o644)
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, env, "commit", "-q", "-m", "add two")
	mustGit(t, dir, env, "push", "-q", "origin", "feat")
	// main moves on: a compare from the merge base must not show this
	// as a removal on feat.
	mustGit(t, dir, env, "checkout", "-q", "main")
	os.WriteFile(filepath.Join(dir, "g.txt"), []byte("g\n"), 0o644)
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, env, "commit", "-q", "-m", "main moves")
	mustGit(t, dir, env, "push", "-q", "origin", "main")

	status, body := inst.get(t, "/alice/app/compare/main...feat")
	if status != 200 || !strings.Contains(body, "add two") || !strings.Contains(body, `<tr class="add" id="f0-n2">`) || !strings.Contains(body, ">two</td>") || strings.Contains(body, "g.txt") {
		t.Fatalf("compare page: %d\n%s", status, body)
	}
	if status, _ := inst.get(t, "/alice/app/compare/main...nope"); status != 404 {
		t.Fatalf("compare with a missing ref: %d", status)
	}
	out, errOut, code := inst.ssh(t, aliceKey, "", "repo", "diff", "alice/app", "main", "feat")
	if code != 0 || !strings.Contains(out, "+two") || strings.Contains(out, "g.txt") {
		t.Fatalf("repo diff: exit %d\n%s%s", code, out, errOut)
	}
}
