package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// build cancel withdraws a queued build; a running one is the runner's.
// Cancelling a duplicate of a commit that already passed puts that
// result back on the commit.
func TestBuildCancel(t *testing.T) {
	inst := startInstance(t)
	inst.runner = buildRunner(t)
	aliceKey := inst.newKey(t, "alice")
	bobKey := inst.newKey(t, "bob")
	runnerKey := inst.newKey(t, "ci")
	inst.admin(t, "admin", "user", "create", "alice", "--key", aliceKey+".pub")
	inst.admin(t, "admin", "user", "create", "bob", "--key", bobKey+".pub")
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
	mustGit(t, dir, env, "commit", "-q", "-m", "ci")
	mustGit(t, dir, env, "push", "-q", "origin", "main")
	sha := strings.Fields(strings.TrimSpace(mustGit(t, dir, env, "ls-remote", "origin", "refs/heads/main")))[0]

	// Build 1 is queued. A reader cannot cancel it; the owner can.
	if _, _, code := inst.ssh(t, bobKey, "", "build", "cancel", "alice/app", "1"); code != 4 {
		t.Fatal("reader cancelled a build")
	}
	if out, errOut, code := inst.ssh(t, aliceKey, "", "build", "cancel", "alice/app", "1"); code != 0 || !strings.Contains(out, "cancelled alice/app build 1") {
		t.Fatalf("cancel: exit %d %s%s", code, out, errOut)
	}
	if list := inst.buildList(t, aliceKey); !strings.Contains(list, "unit\tcancelled") {
		t.Fatalf("build list after cancel:\n%s", list)
	}
	if out, _, _ := inst.ssh(t, aliceKey, "", "build", "log", "alice/app", "1"); !strings.Contains(out, "cancelled by alice") {
		t.Fatalf("log after cancel:\n%s", out)
	}
	if st, _, _ := inst.ssh(t, aliceKey, "", "status", "list", "alice/app", sha); !strings.Contains(st, "error") || !strings.Contains(st, "cancelled") {
		t.Fatalf("status after cancel:\n%s", st)
	}
	if _, _, code := inst.ssh(t, aliceKey, "", "build", "cancel", "alice/app", "1"); code != 2 {
		t.Fatal("cancelled a cancelled build")
	}
	// Nothing for a runner to claim.
	if out := inst.runnerOnce(t, runnerKey); strings.Contains(out, "unit") && !strings.Contains(out, "no pending") {
		t.Fatalf("runner picked up a cancelled build:\n%s", out)
	}
	if status, _ := inst.get(t, "/alice/app/badge/build.svg"); status != 200 {
		t.Fatalf("badge after cancel: %d", status)
	}

	// Build 2: triggered, run to success. Build 3: the same commit queued
	// again; cancelling it restores the passed result on the commit.
	if _, _, code := inst.ssh(t, aliceKey, "", "build", "trigger", "alice/app", "unit"); code != 0 {
		t.Fatal("trigger failed")
	}
	inst.runnerOnce(t, runnerKey)
	if list := inst.buildList(t, aliceKey); !strings.Contains(list, "unit\tsuccess") {
		t.Fatalf("build 2 did not pass:\n%s", list)
	}
	if _, _, code := inst.ssh(t, aliceKey, "", "build", "trigger", "alice/app", "unit"); code != 0 {
		t.Fatal("second trigger failed")
	}
	if st, _, _ := inst.ssh(t, aliceKey, "", "status", "list", "alice/app", sha); !strings.Contains(st, "pending") {
		t.Fatalf("status before cancelling the duplicate:\n%s", st)
	}
	if _, errOut, code := inst.ssh(t, aliceKey, "", "build", "cancel", "alice/app", "3"); code != 0 {
		t.Fatalf("cancel duplicate: %s", errOut)
	}
	if st, _, _ := inst.ssh(t, aliceKey, "", "status", "list", "alice/app", sha); !strings.Contains(st, "success") || !strings.Contains(st, "passed in build 2") {
		t.Fatalf("status after cancelling the duplicate:\n%s", st)
	}
	// A running build cannot be cancelled here.
	if _, _, code := inst.ssh(t, aliceKey, "", "build", "trigger", "alice/app", "unit"); code != 0 {
		t.Fatal("third trigger failed")
	}
	if _, _, code := inst.ssh(t, runnerKey, "", "runner", "next"); code != 0 {
		t.Fatal("claim failed")
	}
	if _, errOut, code := inst.ssh(t, aliceKey, "", "build", "cancel", "alice/app", "4"); code != 2 || !strings.Contains(errOut, "running") {
		t.Fatalf("cancelled a running build: exit %d %s", code, errOut)
	}
}
