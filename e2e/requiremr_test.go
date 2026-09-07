package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// With require-mr on, a protected branch takes changes through mr merge
// only: direct pushes and commit-file are refused, a branch that does
// not exist yet can still be created, and unprotected branches are
// unaffected (#197).
func TestRequireMR(t *testing.T) {
	inst := startInstance(t)
	aliceKey := inst.newKey(t, "alice")
	inst.admin(t, "admin", "user", "create", "alice",
		"--key", aliceKey+".pub", "--email", "alice@example.test", "--verified")
	if _, errOut, code := inst.ssh(t, aliceKey, "", "repo", "create", "alice/lib"); code != 0 {
		t.Fatalf("repo create: %s", errOut)
	}
	env := inst.gitEnv(aliceKey)
	work := t.TempDir()
	mustGit(t, work, env, "clone", "-q", inst.sshURL("alice/lib"), "lib")
	dir := filepath.Join(work, "lib")
	write := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		mustGit(t, dir, env, "add", name)
		mustGit(t, dir, env, "commit", "-q", "-m", name)
	}
	mustGit(t, dir, env, "checkout", "-q", "-b", "main")
	write("README", "one\n")
	mustGit(t, dir, env, "push", "-q", "origin", "main")

	for _, args := range [][]string{
		{"repo", "settings", "protect", "alice/lib", "main"},
		{"repo", "settings", "protect", "alice/lib", "release"},
		{"repo", "settings", "require-mr", "alice/lib", "on"},
	} {
		if _, errOut, code := inst.ssh(t, aliceKey, "", args...); code != 0 {
			t.Fatalf("%v: %s", args, errOut)
		}
	}
	out, _, _ := inst.ssh(t, aliceKey, "", "repo", "settings", "show", "alice/lib", "--json")
	if !strings.Contains(out, `"require_mr":true`) {
		t.Fatalf("settings show: %s", out)
	}

	// Direct push to main: refused, with the reason.
	write("two", "two\n")
	if out, code := gitRun(t, dir, env, "push", "origin", "main"); code == 0 || !strings.Contains(out, "merge requests only") {
		t.Fatalf("direct push to protected branch: %d\n%s", code, out)
	}
	if _, errOut, code := inst.ssh(t, aliceKey, "x\n", "repo", "commit-file", "alice/lib", "notes.txt",
		"--ref", "main", "--file", "-"); code != 4 || !strings.Contains(errOut, "merge requests only") {
		t.Fatalf("commit-file to protected branch: %d %s", code, errOut)
	}
	// Creating a protected branch is still a push; so is an unprotected one.
	mustGit(t, dir, env, "push", "-q", "origin", "main:release")
	mustGit(t, dir, env, "push", "-q", "origin", "main:feature")

	// The same change lands through a merge request.
	if _, errOut, code := inst.ssh(t, aliceKey, "", "mr", "create", "alice/lib",
		"--source", "feature", "--target", "main", "--title", "'two'"); code != 0 {
		t.Fatalf("mr create: %s", errOut)
	}
	if _, errOut, code := inst.ssh(t, aliceKey, "", "mr", "merge", "alice/lib", "1", "--strategy", "ff"); code != 0 {
		t.Fatalf("mr merge: %s", errOut)
	}
	if out, _, _ := inst.ssh(t, aliceKey, "", "repo", "log", "alice/lib", "--limit", "1"); !strings.Contains(out, "two") {
		t.Fatalf("merge did not move main: %s", out)
	}

	// Off again, and the push goes through.
	if _, errOut, code := inst.ssh(t, aliceKey, "", "repo", "settings", "require-mr", "alice/lib", "off"); code != 0 {
		t.Fatalf("require-mr off: %s", errOut)
	}
	write("three", "three\n")
	mustGit(t, dir, env, "push", "-q", "origin", "main")
}
