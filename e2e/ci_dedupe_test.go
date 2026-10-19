package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A fast-forward lands a commit already built on its branch, and it is
// built again for the target: a job sees GITBAY_REF, so the branch's
// result does not say what the job does on main (#335). That holds for
// a branch build that passed, one still queued, and one that failed.
func TestFastForwardMergeBuildsOnTarget(t *testing.T) {
	t.Parallel()
	inst := startInstance(t)
	inst.runner = buildRunner(t)
	aliceKey := inst.newKey(t, "alice")
	runnerKey := inst.newKey(t, "ci")
	inst.admin(t, "admin", "user", "create", "alice", "--key", aliceKey+".pub", "--email", "alice@example.test", "--verified")
	inst.admin(t, "admin", "user", "create", "ci", "--key", runnerKey+".pub", "--admin")
	if _, errOut, code := inst.ssh(t, aliceKey, "", "repo", "create", "alice/app"); code != 0 {
		t.Fatalf("repo create: %s", errOut)
	}
	mainBuilds := func() int {
		out, _, _ := inst.ssh(t, aliceKey, "", "build", "list", "alice/app", "--ref", "main")
		return strings.Count(out, "\n")
	}
	work := t.TempDir()
	env := inst.gitEnv(aliceKey)
	mustGit(t, work, env, "clone", inst.sshURL("alice/app"), "w")
	dir := filepath.Join(work, "w")
	os.MkdirAll(filepath.Join(dir, ".gitbay"), 0o755)
	os.WriteFile(filepath.Join(dir, ".gitbay", "ci.yml"), []byte("jobs:\n  unit:\n    steps:\n      - test -f f.txt || test ! -f fail.txt\n"), 0o644)
	mustGit(t, dir, env, "checkout", "-q", "-b", "main")
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, env, "commit", "-q", "-m", "base")
	mustGit(t, dir, env, "push", "-q", "origin", "main")
	inst.runnerOnce(t, runnerKey) // main's own build

	// A branch whose build passes.
	mustGit(t, dir, env, "checkout", "-q", "-b", "good")
	os.WriteFile(filepath.Join(dir, "f.txt"), []byte("x\n"), 0o644)
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, env, "commit", "-q", "-m", "good change")
	mustGit(t, dir, env, "push", "-q", "origin", "good")
	inst.runnerOnce(t, runnerKey)
	if list := inst.buildList(t, aliceKey); !strings.Contains(list, "unit\tsuccess") {
		t.Fatalf("branch build did not pass:\n%s", list)
	}
	if _, errOut, code := inst.ssh(t, aliceKey, "", "mr", "create", "alice/app", "--source", "good", "--target", "main", "--title", "good"); code != 0 {
		t.Fatalf("mr create: %s", errOut)
	}
	before := mainBuilds()
	if _, errOut, code := inst.ssh(t, aliceKey, "", "mr", "merge", "alice/app", "1", "--strategy", "ff"); code != 0 {
		t.Fatalf("mr merge: %s", errOut)
	}
	if after := mainBuilds(); after != before+1 {
		t.Fatalf("fast-forward of a passed commit queued %d builds on main, want 1:\n%s", after-before, inst.buildList(t, aliceKey))
	}
	inst.runnerOnce(t, runnerKey)

	// A branch merged before its build has run: main gets its own.
	mustGit(t, dir, env, "checkout", "-q", "-b", "early")
	os.WriteFile(filepath.Join(dir, "g.txt"), []byte("y\n"), 0o644)
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, env, "commit", "-q", "-m", "early change")
	mustGit(t, dir, env, "push", "-q", "origin", "early")
	if _, errOut, code := inst.ssh(t, aliceKey, "", "mr", "create", "alice/app", "--source", "early", "--target", "main", "--title", "early"); code != 0 {
		t.Fatalf("mr create: %s", errOut)
	}
	before = mainBuilds()
	if _, errOut, code := inst.ssh(t, aliceKey, "", "mr", "merge", "alice/app", "2", "--strategy", "ff"); code != 0 {
		t.Fatalf("mr merge early: %s", errOut)
	}
	if after := mainBuilds(); after != before+1 {
		t.Fatalf("a merge before the branch build ran queued %d builds on main, want 1:\n%s", after-before, inst.buildList(t, aliceKey))
	}
	inst.runnerOnce(t, runnerKey) // the branch's
	inst.runnerOnce(t, runnerKey) // main's

	// A branch whose build fails.
	mustGit(t, dir, env, "checkout", "-q", "-b", "bad")
	os.WriteFile(filepath.Join(dir, "fail.txt"), []byte("x\n"), 0o644)
	os.Remove(filepath.Join(dir, "f.txt"))
	mustGit(t, dir, env, "add", "-A")
	mustGit(t, dir, env, "commit", "-q", "-m", "bad change")
	mustGit(t, dir, env, "push", "-q", "origin", "bad")
	inst.runnerOnce(t, runnerKey)
	if list := inst.buildList(t, aliceKey); !strings.Contains(list, "unit\tfailure") {
		t.Fatalf("bad branch build did not fail:\n%s", list)
	}
	if _, errOut, code := inst.ssh(t, aliceKey, "", "mr", "create", "alice/app", "--source", "bad", "--target", "main", "--title", "bad"); code != 0 {
		t.Fatalf("mr create: %s", errOut)
	}
	before = mainBuilds()
	if _, errOut, code := inst.ssh(t, aliceKey, "", "mr", "merge", "alice/app", "3", "--strategy", "ff"); code != 0 {
		t.Fatalf("mr merge bad: %s", errOut)
	}
	if after := mainBuilds(); after != before+1 {
		t.Fatalf("a failed commit queued %d builds on main, want 1:\n%s", after-before, inst.buildList(t, aliceKey))
	}
}
