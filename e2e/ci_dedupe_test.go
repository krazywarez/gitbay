package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A fast-forward lands the exact commit that was already built on its
// branch; that commit is not built again. A commit whose build failed is.
func TestFastForwardMergeSkipsBuiltCommit(t *testing.T) {
	inst := startInstance(t)
	inst.runner = buildRunner(t)
	aliceKey := inst.newKey(t, "alice")
	runnerKey := inst.newKey(t, "ci")
	inst.admin(t, "admin", "user", "create", "alice", "--key", aliceKey+".pub", "--email", "alice@example.test", "--verified")
	inst.admin(t, "admin", "user", "create", "ci", "--key", runnerKey+".pub", "--admin")
	if _, errOut, code := inst.ssh(t, aliceKey, "", "repo", "create", "alice/app"); code != 0 {
		t.Fatalf("repo create: %s", errOut)
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
	list := inst.buildList(t, aliceKey)
	if !strings.Contains(list, "good\tunit\tsuccess") && !strings.Contains(list, "unit\tsuccess") {
		t.Fatalf("branch build did not pass:\n%s", list)
	}
	if _, errOut, code := inst.ssh(t, aliceKey, "", "mr", "create", "alice/app", "--source", "good", "--target", "main", "--title", "good"); code != 0 {
		t.Fatalf("mr create: %s", errOut)
	}
	before := strings.Count(inst.buildList(t, aliceKey), "\n")
	if _, errOut, code := inst.ssh(t, aliceKey, "", "mr", "merge", "alice/app", "1", "--strategy", "ff"); code != 0 {
		t.Fatalf("mr merge: %s", errOut)
	}
	if after := strings.Count(inst.buildList(t, aliceKey), "\n"); after != before {
		t.Fatalf("fast-forward queued a build for an already-built commit:\n%s", inst.buildList(t, aliceKey))
	}
	sha := strings.Fields(strings.TrimSpace(mustGit(t, dir, env, "ls-remote", "origin", "refs/heads/main")))[0]
	if status, _, _ := inst.ssh(t, aliceKey, "", "status", "list", "alice/app", sha); !strings.Contains(status, "success") || strings.Contains(status, "pending") {
		t.Fatalf("statuses on the merged commit changed:\n%s", status)
	}

	// A branch whose build fails is built again when it lands.
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
	before = strings.Count(inst.buildList(t, aliceKey), "\n")
	if _, errOut, code := inst.ssh(t, aliceKey, "", "mr", "merge", "alice/app", "2", "--strategy", "ff"); code != 0 {
		t.Fatalf("mr merge bad: %s", errOut)
	}
	if after := strings.Count(inst.buildList(t, aliceKey), "\n"); after != before+1 {
		t.Fatalf("a failed commit was not rebuilt on landing:\n%s", inst.buildList(t, aliceKey))
	}
}
