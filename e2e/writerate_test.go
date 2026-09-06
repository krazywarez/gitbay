package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Mutating commands are bounded per account in the dispatcher, so the
// budget is the same one whichever surface spends it. Reads are not
// charged (#148).
func TestWriteRateLimit(t *testing.T) {
	inst := startInstanceWith(t, "[limits]\nwrite_rate = 4\n")
	aliceKey := inst.newKey(t, "alice")
	bobKey := inst.newKey(t, "bob")
	inst.admin(t, "admin", "user", "create", "alice", "--key", aliceKey+".pub")
	inst.admin(t, "admin", "user", "create", "bob", "--key", bobKey+".pub")

	// repo create plus three issues is the whole budget.
	if _, errOut, code := inst.ssh(t, aliceKey, "", "repo", "create", "alice/app"); code != 0 {
		t.Fatalf("repo create: %s", errOut)
	}
	for i := 0; i < 3; i++ {
		if _, errOut, code := inst.ssh(t, aliceKey, "", "issue", "create", "alice/app", "--title", "t"); code != 0 {
			t.Fatalf("issue %d within the budget was refused: %s", i+1, errOut)
		}
	}
	_, errOut, code := inst.ssh(t, aliceKey, "", "issue", "create", "alice/app", "--title", "over")
	if code != 4 {
		t.Fatalf("write past the budget exited %d, want 4:\n%s", code, errOut)
	}
	if !strings.Contains(errOut, "too many writes") || !strings.Contains(errOut, "try again in") {
		t.Errorf("refusal does not say what happened or when to retry: %s", errOut)
	}

	// Reads are not charged, so a throttled account can still look.
	if out, _, code := inst.ssh(t, aliceKey, "", "issue", "list", "alice/app"); code != 0 {
		t.Fatalf("a read was refused while throttled: %s", out)
	}

	// The budget is per account, not per instance.
	if _, errOut, code := inst.ssh(t, bobKey, "", "repo", "create", "bob/app"); code != 0 {
		t.Fatalf("bob was charged for alice's writes: %s", errOut)
	}
}

// The runner protocol is exempt: a build streams its log in many small
// writes, and throttling those would throttle CI itself.
func TestWriteRateLimitSparesTheRunner(t *testing.T) {
	inst := startInstanceWith(t, "[limits]\nwrite_rate = 2\n")
	inst.runner = buildRunner(t)
	aliceKey := inst.newKey(t, "alice")
	inst.admin(t, "admin", "user", "create", "alice", "--key", aliceKey+".pub")
	runnerKey := inst.newKey(t, "ci")
	inst.admin(t, "admin", "user", "create", "ci", "--key", runnerKey+".pub", "--admin")

	if _, errOut, code := inst.ssh(t, aliceKey, "", "repo", "create", "alice/app"); code != 0 {
		t.Fatalf("repo create: %s", errOut)
	}
	env := inst.gitEnv(aliceKey)
	work := t.TempDir()
	mustGit(t, work, env, "clone", inst.sshURL("alice/app"), "w")
	dir := filepath.Join(work, "w")
	os.MkdirAll(filepath.Join(dir, ".gitbay"), 0o755)
	os.WriteFile(filepath.Join(dir, ".gitbay", "ci.yml"),
		[]byte("jobs:\n  smoke:\n    steps:\n      - echo ok\n"), 0o644)
	mustGit(t, dir, env, "checkout", "-q", "-b", "main")
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, env, "commit", "-q", "-m", "base")
	mustGit(t, dir, env, "push", "-q", "origin", "main")

	inst.runnerOnce(t, runnerKey)
	out, _, _ := inst.ssh(t, aliceKey, "", "build", "list", "alice/app")
	if !strings.Contains(out, "success") {
		t.Fatalf("the build did not run under a tight write budget:\n%s", out)
	}
}
