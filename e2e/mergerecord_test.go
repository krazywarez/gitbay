package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A merge request whose head is pushed onto its target by hand is
// recorded as merged by the push (#330); mr merge on it then reports the
// state rather than merging again.
func TestMergeRecordedWhenPushLandsHead(t *testing.T) {
	t.Parallel()
	inst := startInstance(t)
	aliceKey := inst.newKey(t, "alice")
	inst.admin(t, "admin", "user", "create", "alice", "--key", aliceKey+".pub")
	if _, errOut, code := inst.ssh(t, aliceKey, "", "repo", "create", "alice/app"); code != 0 {
		t.Fatalf("repo create: %s", errOut)
	}
	work := t.TempDir()
	env := inst.gitEnv(aliceKey)
	mustGit(t, work, env, "clone", inst.sshURL("alice/app"), "w")
	dir := filepath.Join(work, "w")
	os.WriteFile(filepath.Join(dir, "f.txt"), []byte("x\n"), 0o644)
	mustGit(t, dir, env, "checkout", "-q", "-b", "main")
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, env, "commit", "-q", "-m", "base")
	mustGit(t, dir, env, "push", "-q", "origin", "main")
	mustGit(t, dir, env, "checkout", "-q", "-b", "feat")
	os.WriteFile(filepath.Join(dir, "f.txt"), []byte("y\n"), 0o644)
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, env, "commit", "-q", "-m", "change")
	mustGit(t, dir, env, "push", "-q", "origin", "feat")
	if _, errOut, code := inst.ssh(t, aliceKey, "", "mr", "create", "alice/app", "--source", "feat", "--target", "main", "--title", "change"); code != 0 {
		t.Fatalf("mr create: %s", errOut)
	}
	// Merged by hand: main fast-forwards to the branch outside the forge.
	mustGit(t, dir, env, "push", "-q", "origin", "feat:main")

	show, _, _ := inst.ssh(t, aliceKey, "", "mr", "show", "alice/app", "1", "--json")
	if !strings.Contains(show, `"state":"merged"`) || !strings.Contains(show, `"merged_by":"alice"`) {
		t.Fatalf("merge request not recorded as merged by the push:\n%s", show)
	}
	if out, errOut, code := inst.ssh(t, aliceKey, "", "mr", "merge", "alice/app", "1"); code == 0 || !strings.Contains(errOut, "is merged") {
		t.Fatalf("merge of a merged MR: exit %d\n%s%s", code, out, errOut)
	}
}
