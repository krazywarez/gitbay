package e2e

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
}

// Cancelling a running build ends it at the runner within seconds: the
// server closes the log session, the runner kills the step, and its late
// report lands on a row that already says cancelled.
func TestBuildCancelRunning(t *testing.T) {
	inst := startInstance(t)
	inst.runner = buildRunner(t)
	aliceKey := inst.newKey(t, "alice")
	runnerKey := inst.newKey(t, "ci")
	inst.admin(t, "admin", "user", "create", "alice", "--key", aliceKey+".pub")
	inst.admin(t, "admin", "user", "create", "ci", "--key", runnerKey+".pub", "--admin")
	if _, _, code := inst.ssh(t, aliceKey, "", "repo", "create", "alice/slow"); code != 0 {
		t.Fatal("repo create failed")
	}
	work := t.TempDir()
	env := inst.gitEnv(aliceKey)
	mustGit(t, work, env, "clone", inst.sshURL("alice/slow"), "w")
	dir := filepath.Join(work, "w")
	os.MkdirAll(filepath.Join(dir, ".gitbay"), 0o755)
	os.WriteFile(filepath.Join(dir, ".gitbay", "ci.yml"), []byte("jobs:\n  slow:\n    steps:\n      - echo starting\n      - sleep 120\n      - echo never\n"), 0o644)
	mustGit(t, dir, env, "checkout", "-q", "-b", "main")
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, env, "commit", "-q", "-m", "slow")
	mustGit(t, dir, env, "push", "-q", "origin", "main")
	sha := strings.Fields(strings.TrimSpace(mustGit(t, dir, env, "ls-remote", "origin", "refs/heads/main")))[0]
	slowList := func() string {
		out, _, _ := inst.ssh(t, aliceKey, "", "build", "list", "alice/slow")
		return out
	}

	// A real runner, in the background, claims the build and sits in sleep.
	opts := fmt.Sprintf("-p %d -i %s -o IdentitiesOnly=yes -o StrictHostKeyChecking=no -o UserKnownHostsFile=%s -o BatchMode=yes",
		inst.port, runnerKey, filepath.Join(inst.sshDir, "known_hosts"))
	runner := exec.Command(inst.runner, "-once", "-remote", "git@127.0.0.1", "-ssh-opts", opts,
		"-clone-base", fmt.Sprintf("ssh://git@127.0.0.1:%d", inst.port), "-workdir", t.TempDir())
	runner.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
	var runnerOut strings.Builder
	runner.Stdout, runner.Stderr = &runnerOut, &runnerOut
	if err := runner.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- runner.Wait() }()
	t.Cleanup(func() { runner.Process.Kill() })
	deadline := time.Now().Add(30 * time.Second)
	for !strings.Contains(slowList(), "slow\trunning") {
		if time.Now().After(deadline) {
			t.Fatalf("runner never claimed the build:\n%s\nbuild list:\n%s", runnerOut.String(), slowList())
		}
		time.Sleep(200 * time.Millisecond)
	}
	started := time.Now()
	out, errOut, code := inst.ssh(t, aliceKey, "", "build", "cancel", "alice/slow", "1")
	if code != 0 || !strings.Contains(out, "the runner stops at its next check") {
		t.Fatalf("cancel running: exit %d %s%s", code, out, errOut)
	}
	select {
	case <-exited:
	case <-time.After(20 * time.Second):
		t.Fatalf("runner still running 20s after cancel:\n%s", runnerOut.String())
	}
	if took := time.Since(started); took > 15*time.Second {
		t.Fatalf("runner took %s to stop", took)
	}
	if !strings.Contains(runnerOut.String(), "cancelled") {
		t.Fatalf("runner did not say it was cancelled:\n%s", runnerOut.String())
	}
	// The row, log and status say cancelled, and the runner's late report
	// changed none of them.
	if list := slowList(); !strings.Contains(list, "slow\tcancelled") {
		t.Fatalf("build after cancel:\n%s", list)
	}
	log, _, _ := inst.ssh(t, aliceKey, "", "build", "log", "alice/slow", "1")
	if !strings.Contains(log, "starting") || !strings.Contains(log, "cancelled by alice while running") || strings.Contains(log, "never") {
		t.Fatalf("log after cancel:\n%s", log)
	}
	if st, _, _ := inst.ssh(t, aliceKey, "", "status", "list", "alice/slow", sha); !strings.Contains(st, "error") || !strings.Contains(st, "cancelled") {
		t.Fatalf("status after cancel:\n%s", st)
	}
}
