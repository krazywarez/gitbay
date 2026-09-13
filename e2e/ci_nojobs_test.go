package e2e

import (
	"os"
	"path/filepath"
	"testing"
)

// require_checks on a repository with no CI configuration used to refuse
// every merge with "none were reported", and the only way out was turning
// the setting off. Nothing was ever going to report, so the gate has
// nothing to wait for.
func TestRequireChecksWithoutCIConfigMerges(t *testing.T) {
	inst := startInstance(t)
	aliceKey := inst.newKey(t, "alice")
	inst.admin(t, "admin", "user", "create", "alice", "--key", aliceKey+".pub",
		"--email", "alice@example.test", "--verified")

	if _, errOut, code := inst.ssh(t, aliceKey, "", "repo", "create", "alice/app"); code != 0 {
		t.Fatalf("repo create: %s", errOut)
	}
	if _, errOut, code := inst.ssh(t, aliceKey, "", "repo", "settings", "require-checks", "alice/app", "on"); code != 0 {
		t.Fatalf("require-checks on: %s", errOut)
	}

	work := t.TempDir()
	env := inst.gitEnv(aliceKey)
	mustGit(t, work, env, "clone", inst.sshURL("alice/app"), "w")
	dir := filepath.Join(work, "w")
	os.WriteFile(filepath.Join(dir, "README"), []byte("x\n"), 0o644)
	mustGit(t, dir, env, "checkout", "-q", "-b", "main")
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, env, "commit", "-q", "-m", "base")
	mustGit(t, dir, env, "push", "-q", "origin", "main")

	mustGit(t, dir, env, "checkout", "-q", "-b", "feature")
	os.WriteFile(filepath.Join(dir, "README"), []byte("y\n"), 0o644)
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, env, "commit", "-q", "-m", "change")
	mustGit(t, dir, env, "push", "-q", "origin", "feature")

	if _, errOut, code := inst.ssh(t, aliceKey, "", "mr", "create", "alice/app",
		"--source", "feature", "--target", "main", "--title", "change"); code != 0 {
		t.Fatalf("mr create: %s", errOut)
	}
	if out, errOut, code := inst.ssh(t, aliceKey, "", "mr", "merge", "alice/app", "1",
		"--strategy", "merge"); code != 0 {
		t.Fatalf("mr merge refused under require_checks with no CI: %s %s", out, errOut)
	}
}
