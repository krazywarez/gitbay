package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Signed commits mean only fast-forward merges are allowed, so a branch
// whose target advances gets rebased and force-pushed — orphaning whatever
// was queued for the old head. That build must not run and fail at clone
// looking like a real failure; it is cancelled when a runner claims it, and
// the runner gets the real build behind it instead, in the same poll.
func TestBuildOrphanedByForcePushCancelledAtClaim(t *testing.T) {
	inst := startInstance(t)
	inst.runner = buildRunner(t)
	aliceKey := inst.newKey(t, "alice")
	runnerKey := inst.newKey(t, "ci")
	inst.admin(t, "admin", "user", "create", "alice", "--key", aliceKey+".pub")
	inst.admin(t, "admin", "user", "create", "ci", "--key", runnerKey+".pub", "--admin")
	if _, _, code := inst.ssh(t, aliceKey, "", "repo", "create", "alice/app"); code != 0 {
		t.Fatal("repo create failed")
	}
	work := t.TempDir()
	env := inst.gitEnv(aliceKey)
	mustGit(t, work, env, "clone", inst.sshURL("alice/app"), "w")
	dir := filepath.Join(work, "w")
	os.MkdirAll(filepath.Join(dir, ".gitbay"), 0o755)
	os.WriteFile(filepath.Join(dir, ".gitbay", "ci.yml"), []byte("jobs:\n  unit:\n    steps:\n      - echo fine\n"), 0o644)
	mustGit(t, dir, env, "checkout", "-q", "-b", "main")
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, env, "commit", "-q", "-m", "base")
	mustGit(t, dir, env, "push", "-q", "origin", "main")
	orphanSHA := strings.TrimSpace(mustGit(t, dir, env, "rev-parse", "HEAD"))

	// Amend and force-push: build 1 (queued for orphanSHA) is left behind,
	// unreachable once main moves; build 2 queues for the amended commit.
	mustGit(t, dir, env, "commit", "-q", "--amend", "-m", "amended")
	mustGit(t, dir, env, "push", "-q", "--force", "origin", "main")
	realSHA := strings.TrimSpace(mustGit(t, dir, env, "rev-parse", "HEAD"))
	if orphanSHA == realSHA {
		t.Fatal("amend produced the same sha; test setup is broken")
	}

	out, _, _ := inst.ssh(t, aliceKey, "", "build", "list", "alice/app")
	if !strings.Contains(out, "1\tunit\tpending") || !strings.Contains(out, "2\tunit\tpending") {
		t.Fatalf("expected both builds queued and pending:\n%s", out)
	}

	// One claim: the orphaned build is cancelled and the real one runs,
	// without a second poll.
	inst.runnerOnce(t, runnerKey)

	out, _, _ = inst.ssh(t, aliceKey, "", "build", "list", "alice/app")
	if !strings.Contains(out, "1\tunit\tcancelled") {
		t.Fatalf("orphaned build not cancelled:\n%s", out)
	}
	if !strings.Contains(out, "2\tunit\tsuccess") {
		t.Fatalf("real build behind it did not run:\n%s", out)
	}

	log, _, _ := inst.ssh(t, aliceKey, "", "build", "log", "alice/app", "1")
	if !strings.Contains(log, "not reachable") {
		t.Fatalf("cancelled build's log does not explain why:\n%s", log)
	}

	st, _, _ := inst.ssh(t, aliceKey, "", "status", "list", "alice/app", orphanSHA)
	if !strings.Contains(st, "ci/unit") || strings.Contains(st, "pending") {
		t.Fatalf("orphaned commit's status left pending:\n%s", st)
	}
}
