package e2e

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A build claimed by a runner that never reports is failed by the
// scheduler's tick, with no runner alive to trigger it.
func TestStaleBuildReapedWithoutRunner(t *testing.T) {
	t.Setenv("GITBAY_SCHED_TICK", "500ms")
	t.Setenv("GITBAY_STALE_BUILD_DEADLINE", "2s")
	inst := startInstance(t)
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
	os.WriteFile(filepath.Join(dir, ".gitbay", "ci.yml"), []byte("jobs:\n  ok:\n    steps:\n      - echo fine\n"), 0o644)
	mustGit(t, dir, env, "checkout", "-q", "-b", "main")
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, env, "commit", "-q", "-m", "ci")
	mustGit(t, dir, env, "push", "-q", "origin", "main")

	// Claim the build the way a runner does, then vanish.
	out, errOut, code := inst.ssh(t, runnerKey, "", "runner", "next", "--json")
	if code != 0 || !strings.Contains(out, `"job":"ok"`) {
		t.Fatalf("runner next: exit %d\n%s%s", code, out, errOut)
	}
	var claim struct {
		Data struct {
			SHA string `json:"sha"`
		} `json:"data"`
	}
	json.Unmarshal([]byte(out), &claim)

	status := func() (string, string) {
		out, _, _ := inst.ssh(t, aliceKey, "", "build", "list", "alice/app", "--json")
		var env struct {
			Data []struct {
				Status string `json:"status"`
			} `json:"data"`
		}
		json.Unmarshal([]byte(out), &env)
		st := ""
		if len(env.Data) > 0 {
			st = env.Data[0].Status
		}
		cs, _, _ := inst.ssh(t, aliceKey, "", "status", "list", "alice/app", claim.Data.SHA)
		return st, cs
	}
	if st, _ := status(); st != "running" {
		t.Fatalf("claimed build is %q, want running", st)
	}
	deadline := time.Now().Add(20 * time.Second)
	for {
		st, cs := status()
		if st == "failure" && strings.Contains(cs, "build abandoned") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("never reaped: build %q, statuses:\n%s", st, cs)
		}
		time.Sleep(250 * time.Millisecond)
	}
	if out, _, _ := inst.ssh(t, aliceKey, "", "build", "log", "alice/app", "1"); !strings.Contains(out, "build abandoned") {
		t.Fatalf("log lacks the abandonment note:\n%s", out)
	}
}
