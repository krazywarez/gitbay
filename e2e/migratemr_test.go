package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestMigratedMRHasADiff covers #128's concrete symptom: a merge request
// replayed from a bundle used to arrive with an empty head, so `mr diff`
// on it could only fail. The bundle carries the head and the merge base
// now, and the head ref is set once the git objects are pushed.
//
// The merge request here is merged and its source branch deleted, which
// is the case that actually breaks. While the branch still exists the
// diff resolves through it and an empty head_sha is invisible — the first
// version of this test made that mistake and passed without the fix.
func TestMigratedMRHasADiff(t *testing.T) {
	src := startInstance(t)
	dst := startInstance(t)
	key := src.newKey(t, "alice")
	src.admin(t, "admin", "user", "create", "alice", "--key", key+".pub")
	dst.admin(t, "admin", "user", "create", "alice", "--key", key+".pub")

	if _, errOut, code := src.ssh(t, key, "", "repo", "create", "alice/tool"); code != 0 {
		t.Fatalf("repo create: %s", errOut)
	}
	env := src.gitEnv(key)
	work := t.TempDir()
	mustGit(t, work, env, "clone", src.sshURL("alice/tool"), "w")
	dir := filepath.Join(work, "w")
	os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o644)
	mustGit(t, dir, env, "checkout", "-q", "-b", "main")
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, env, "commit", "-q", "-m", "base")
	mustGit(t, dir, env, "push", "-q", "origin", "main")

	mustGit(t, dir, env, "checkout", "-q", "-b", "feat")
	os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n\nfunc F() {}\n"), 0o644)
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, env, "commit", "-q", "-m", "add F")
	mustGit(t, dir, env, "push", "-q", "origin", "feat")
	if _, errOut, code := src.ssh(t, key, "", "mr", "create", "alice/tool",
		"--source", "feat", "--target", "main", "--title", "'add F'"); code != 0 {
		t.Fatalf("mr create: %s", errOut)
	}
	// Merge it and delete the branch, the way a finished merge request
	// ends up. Now nothing but the recorded head says what it contained.
	if _, errOut, code := src.ssh(t, key, "", "mr", "merge", "alice/tool", "1"); code != 0 {
		t.Fatalf("merge: %s", errOut)
	}
	mustGit(t, dir, env, "push", "-q", "origin", "--delete", "feat")

	// The source's own diff is the standard to match.
	want, errOut, code := src.ssh(t, key, "", "mr", "diff", "alice/tool", "1")
	if code != 0 || !strings.Contains(want, "func F()") {
		t.Fatalf("source diff: %s\n%s", errOut, want)
	}

	bundle, errOut, code := src.ssh(t, key, "", "account", "export")
	if code != 0 {
		t.Fatalf("export: %s", errOut)
	}
	if !strings.Contains(bundle, `"head_sha"`) {
		t.Fatalf("bundle carries no head_sha:\n%s", bundle)
	}
	if _, errOut, code := dst.ssh(t, key, bundle, "account", "import-bundle"); code != 0 {
		t.Fatalf("import: %s", errOut)
	}

	// Git data moves separately, which is the order a real migration runs
	// in: the bundle first, the push after.
	// Only main: the feature branch is gone, exactly as at the source.
	// The merge commit carries the head's objects, so they arrive anyway.
	denv := dst.gitEnv(key)
	mustGit(t, dir, denv, "remote", "add", "dst", dst.sshURL("alice/tool"))
	mustGit(t, dir, denv, "fetch", "-q", "origin", "main")
	mustGit(t, dir, denv, "push", "-q", "dst", "refs/remotes/origin/main:refs/heads/main")

	// Re-importing the same bundle is how the head ref gets set once the
	// objects are present; the merge request itself is already there.
	out, errOut, code := dst.ssh(t, key, bundle, "account", "import-bundle")
	if code != 0 {
		t.Fatalf("re-import: %s", errOut)
	}
	if !strings.Contains(out, "already present") {
		t.Fatalf("re-import did not skip what it had: %s", out)
	}

	show, _, _ := dst.ssh(t, key, "", "mr", "show", "alice/tool", "1", "--json")
	got, errOut, code := dst.ssh(t, key, "", "mr", "diff", "alice/tool", "1")
	if code != 0 {
		t.Fatalf("migrated MR has no diff: %s\n%s\nmr show: %s", errOut, got, show)
	}
	if !strings.Contains(got, "func F()") {
		t.Fatalf("migrated diff does not match the source:\nwant to contain func F()\ngot:\n%s", got)
	}
}
