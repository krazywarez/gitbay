package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Protected-tag globs refuse moving and deleting matching tags; a tag a
// release is anchored to refuses both on its own (#201).
func TestTagProtection(t *testing.T) {
	inst := startInstance(t)
	aliceKey := inst.newKey(t, "alice")
	inst.admin(t, "admin", "user", "create", "alice", "--key", aliceKey+".pub")
	if _, errOut, code := inst.ssh(t, aliceKey, "", "repo", "create", "alice/app"); code != 0 {
		t.Fatalf("repo create: %s", errOut)
	}
	work := t.TempDir()
	env := inst.gitEnv(aliceKey)
	mustGit(t, work, env, "clone", "-q", inst.sshURL("alice/app"), "w")
	dir := filepath.Join(work, "w")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustGit(t, dir, env, "checkout", "-q", "-b", "main")
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, env, "commit", "-q", "-m", "base")
	mustGit(t, dir, env, "tag", "v1.0")
	mustGit(t, dir, env, "tag", "nightly")
	mustGit(t, dir, env, "push", "-q", "origin", "main", "v1.0", "nightly")

	if _, errOut, code := inst.ssh(t, aliceKey, "", "repo", "settings", "protect-tag", "alice/app", "'['"); code != 2 {
		t.Fatalf("bad glob accepted: %d %s", code, errOut)
	}
	if _, errOut, code := inst.ssh(t, aliceKey, "", "repo", "settings", "protect-tag", "alice/app", "'v*'"); code != 0 {
		t.Fatalf("protect-tag: %s", errOut)
	}
	out, _, _ := inst.ssh(t, aliceKey, "", "repo", "settings", "show", "alice/app", "--json")
	if !strings.Contains(out, `"protected_tags":["v*"]`) {
		t.Fatalf("settings show: %s", out)
	}

	// Delete and move are refused for a matching tag; an unmatched tag is
	// free, and a new matching tag can still be created.
	if out, code := gitRun(t, dir, env, "push", "origin", ":v1.0"); code == 0 || !strings.Contains(out, "protected") {
		t.Fatalf("protected tag deleted: %d\n%s", code, out)
	}
	mustGit(t, dir, env, "commit", "-q", "--allow-empty", "-m", "second")
	mustGit(t, dir, env, "tag", "-f", "v1.0")
	if out, code := gitRun(t, dir, env, "push", "--force", "origin", "v1.0"); code == 0 || !strings.Contains(out, "protected") {
		t.Fatalf("protected tag moved: %d\n%s", code, out)
	}
	mustGit(t, dir, env, "push", "-q", "origin", ":nightly")
	mustGit(t, dir, env, "tag", "v1.1")
	mustGit(t, dir, env, "push", "-q", "origin", "v1.1")

	// Unprotected again, the tag can go — unless a release anchors it.
	if _, errOut, code := inst.ssh(t, aliceKey, "", "repo", "settings", "unprotect-tag", "alice/app", "'v*'"); code != 0 {
		t.Fatalf("unprotect-tag: %s", errOut)
	}
	if _, errOut, code := inst.ssh(t, aliceKey, "", "release", "create", "alice/app", "v1.1", "--title", "'one one'"); code != 0 {
		t.Fatalf("release create: %s", errOut)
	}
	if out, code := gitRun(t, dir, env, "push", "origin", ":v1.1"); code == 0 || !strings.Contains(out, "anchors a release") {
		t.Fatalf("release tag deleted: %d\n%s", code, out)
	}
	mustGit(t, dir, env, "commit", "-q", "--allow-empty", "-m", "third")
	mustGit(t, dir, env, "tag", "-f", "v1.1")
	if out, code := gitRun(t, dir, env, "push", "--force", "origin", "v1.1"); code == 0 || !strings.Contains(out, "anchors a release") {
		t.Fatalf("release tag moved: %d\n%s", code, out)
	}
	if _, errOut, code := inst.ssh(t, aliceKey, "", "release", "delete", "alice/app", "v1.1", "--yes"); code != 0 {
		t.Fatalf("release delete: %s", errOut)
	}
	mustGit(t, dir, env, "push", "-q", "origin", ":v1.1")
}
