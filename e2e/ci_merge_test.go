package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A merge moves the target ref without reaching post-receive, so the
// ref-update work has to happen in the merge path. Before it did, merging
// an MR into a repository with a CI config queued nothing, while pushing
// the identical commit ran the whole config.
func TestMergeQueuesBuildsAndRecordsPush(t *testing.T) {
	inst := startInstance(t)
	aliceKey := inst.newKey(t, "alice")
	inst.admin(t, "admin", "user", "create", "alice", "--key", aliceKey+".pub",
		"--email", "alice@example.test", "--verified")

	if _, errOut, code := inst.ssh(t, aliceKey, "", "repo", "create", "alice/app"); code != 0 {
		t.Fatalf("repo create: %s", errOut)
	}
	work := t.TempDir()
	env := inst.gitEnv(aliceKey)
	mustGit(t, work, env, "clone", inst.sshURL("alice/app"), "w")
	dir := filepath.Join(work, "w")
	os.MkdirAll(filepath.Join(dir, ".gitbay"), 0o755)
	os.WriteFile(filepath.Join(dir, ".gitbay", "ci.yml"), []byte(
		"jobs:\n  unit:\n    steps:\n      - echo hello\n"), 0o644)
	mustGit(t, dir, env, "checkout", "-q", "-b", "main")
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, env, "commit", "-q", "-m", "base")
	mustGit(t, dir, env, "push", "-q", "origin", "main")

	// A feature branch, pushed and merged rather than pushed to main.
	mustGit(t, dir, env, "checkout", "-q", "-b", "feature")
	os.WriteFile(filepath.Join(dir, "f.txt"), []byte("x\n"), 0o644)
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, env, "commit", "-q", "-m", "a change")
	mustGit(t, dir, env, "push", "-q", "origin", "feature")

	if _, errOut, code := inst.ssh(t, aliceKey, "", "mr", "create", "alice/app",
		"--source", "feature", "--target", "main", "--title", "'a change'"); code != 0 {
		t.Fatalf("mr create: %s", errOut)
	}
	before := strings.Count(inst.buildList(t, aliceKey), "\n")

	if _, errOut, code := inst.ssh(t, aliceKey, "", "mr", "merge", "alice/app", "1",
		"--strategy", "merge"); code != 0 {
		t.Fatalf("mr merge: %s", errOut)
	}
	merged := strings.TrimSpace(mustGit(t, dir, env, "ls-remote", "origin", "refs/heads/main"))
	sha := strings.Fields(merged)[0]

	// The merge queued the branch's jobs at the merge commit.
	out := inst.buildList(t, aliceKey)
	if strings.Count(out, "\n") <= before {
		t.Fatalf("merge queued no build:\n%s", out)
	}
	if !strings.Contains(out, "unit\tpending") {
		t.Fatalf("merge did not queue the unit job:\n%s", out)
	}
	status, _, _ := inst.ssh(t, aliceKey, "", "status", "list", "alice/app", sha)
	if !strings.Contains(status, "ci/unit") || !strings.Contains(status, "pending") {
		t.Fatalf("no pending status on the merge commit %s:\n%s", sha, status)
	}
}

func (i *instance) buildList(t *testing.T, key string) string {
	t.Helper()
	out, _, _ := i.ssh(t, key, "", "build", "list", "alice/app")
	return out
}
