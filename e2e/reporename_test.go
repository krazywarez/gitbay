package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// repo rename moves the directory and the row together; what hangs off
// the repository by id (issues here) follows it (#190).
func TestRepoRename(t *testing.T) {
	inst := startInstance(t)
	aliceKey := inst.newKey(t, "alice")
	bobKey := inst.newKey(t, "bob")
	inst.admin(t, "admin", "user", "create", "alice", "--key", aliceKey+".pub")
	inst.admin(t, "admin", "user", "create", "bob", "--key", bobKey+".pub")
	if _, errOut, code := inst.ssh(t, aliceKey, "", "repo", "create", "alice/box"); code != 0 {
		t.Fatalf("repo create: %s", errOut)
	}
	if _, errOut, code := inst.ssh(t, aliceKey, "", "issue", "create", "alice/box", "--title", "'keep'"); code != 0 {
		t.Fatalf("issue create: %s", errOut)
	}
	env := inst.gitEnv(aliceKey)
	work := t.TempDir()
	mustGit(t, work, env, "clone", "-q", inst.sshURL("alice/box"), "box")
	dir := filepath.Join(work, "box")
	if err := os.WriteFile(filepath.Join(dir, "README"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustGit(t, dir, env, "checkout", "-q", "-b", "main")
	mustGit(t, dir, env, "add", "README")
	mustGit(t, dir, env, "commit", "-q", "-m", "one")
	mustGit(t, dir, env, "push", "-q", "origin", "main")

	if _, errOut, code := inst.ssh(t, bobKey, "", "repo", "rename", "alice/box", "crate"); code != 4 {
		t.Fatalf("non-admin rename: %d %s", code, errOut)
	}
	if _, errOut, code := inst.ssh(t, aliceKey, "", "repo", "rename", "alice/box", "box"); code != 2 {
		t.Fatalf("rename to the same name: %d %s", code, errOut)
	}
	if _, _, code := inst.ssh(t, aliceKey, "", "repo", "rename", "alice/box", "'bad name'"); code == 0 {
		t.Fatal("invalid name accepted")
	}
	out, errOut, code := inst.ssh(t, aliceKey, "", "repo", "rename", "alice/box", "crate", "--json")
	if code != 0 || !strings.Contains(out, `"repo":"alice/crate"`) {
		t.Fatalf("rename: %d %s %s", code, out, errOut)
	}

	clone := filepath.Join(t.TempDir(), "crate")
	mustGit(t, t.TempDir(), env, "clone", "-q", inst.sshURL("alice/crate"), clone)
	if _, err := os.Stat(filepath.Join(clone, "README")); err != nil {
		t.Fatalf("renamed repository lost its content: %v", err)
	}
	if out, code := gitRun(t, t.TempDir(), env, "clone", "-q", inst.sshURL("alice/box")); code == 0 {
		t.Fatalf("old path still clones:\n%s", out)
	}
	if _, _, code := inst.ssh(t, aliceKey, "", "repo", "show", "alice/box"); code != 3 {
		t.Fatalf("old path still shows: %d", code)
	}
	if _, errOut, code := inst.ssh(t, aliceKey, "", "issue", "show", "alice/crate", "1"); code != 0 {
		t.Fatalf("issue did not follow the rename: %s", errOut)
	}

	// A collision with an existing repository is refused and nothing moves.
	if _, errOut, code := inst.ssh(t, aliceKey, "", "repo", "create", "alice/box"); code != 0 {
		t.Fatalf("recreate box: %s", errOut)
	}
	if _, errOut, code := inst.ssh(t, aliceKey, "", "repo", "rename", "alice/crate", "box"); code == 0 || !strings.Contains(errOut, "already") {
		t.Fatalf("collision rename: %d %s", code, errOut)
	}
	if _, _, code := inst.ssh(t, aliceKey, "", "repo", "show", "alice/crate"); code != 0 {
		t.Fatal("refused rename moved the repository anyway")
	}
}
