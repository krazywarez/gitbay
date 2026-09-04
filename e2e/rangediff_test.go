package e2e

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestMRRangeDiff is #111's last stage: a push stales every review and
// nothing said what had changed between the two heads. A plain diff of
// the heads cannot answer that — it shows the whole branch again.
func TestMRRangeDiff(t *testing.T) {
	inst := startInstance(t)
	key := inst.newKey(t, "alice")
	inst.admin(t, "admin", "user", "create", "alice", "--key", key+".pub")
	if _, errOut, code := inst.ssh(t, key, "", "repo", "create", "alice/app"); code != 0 {
		t.Fatalf("repo create: %s", errOut)
	}
	env := inst.gitEnv(key)
	work := t.TempDir()
	mustGit(t, work, env, "clone", inst.sshURL("alice/app"), "w")
	dir := filepath.Join(work, "w")
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\n"), 0o644)
	mustGit(t, dir, env, "checkout", "-q", "-b", "main")
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, env, "commit", "-q", "-m", "base")
	mustGit(t, dir, env, "push", "-q", "origin", "main")

	mustGit(t, dir, env, "checkout", "-q", "-b", "feat")
	os.WriteFile(filepath.Join(dir, "b.txt"), []byte("alpha\nbeta\ngamma\n"), 0o644)
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, env, "commit", "-q", "-m", "add b")
	mustGit(t, dir, env, "push", "-q", "origin", "feat")
	if _, errOut, code := inst.ssh(t, key, "", "mr", "create", "alice/app",
		"--source", "feat", "--target", "main", "--title", "'add b'"); code != 0 {
		t.Fatalf("mr create: %s", errOut)
	}

	// One revision: nothing to compare, and saying so is not a failure.
	out, errOut, code := inst.ssh(t, key, "", "mr", "range-diff", "alice/app", "1")
	if code != 0 || !strings.Contains(errOut, "one revision") {
		t.Fatalf("single-revision range-diff: exit %d, %s, %s", code, errOut, out)
	}

	// Amend and force-push: the same commit with one line changed, which
	// is what addressing review feedback looks like. A diff of the two
	// heads cannot describe this — it shows the whole branch again.
	os.WriteFile(filepath.Join(dir, "b.txt"), []byte("alpha\nbeta revised\ngamma\n"), 0o644)
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, env, "commit", "-q", "--amend", "--no-edit")
	mustGit(t, dir, env, "push", "-q", "--force", "origin", "feat")

	revs := revisions(t, inst, key)
	if len(revs) != 2 {
		t.Fatalf("revisions = %d, want 2 after a force-push: %+v", len(revs), revs)
	}
	if !revs[1].Current {
		t.Fatalf("the newest revision is not marked current: %+v", revs)
	}

	out, errOut, code = inst.ssh(t, key, "", "mr", "range-diff", "alice/app", "1")
	if code != 0 {
		t.Fatalf("range-diff: %s", errOut)
	}
	// The two versions of the one commit are paired — "1: <old> ! 1:
	// <new>" — and the interdiff shows the one line that moved, not the
	// whole branch.
	if !strings.Contains(out, "add b") {
		t.Fatalf("range-diff does not mention the commit:\n%s", out)
	}
	if !strings.Contains(out, "!") {
		t.Fatalf("range-diff did not pair the amended commit:\n%s", out)
	}
	if !strings.Contains(out, "beta revised") {
		t.Fatalf("range-diff does not show the changed line:\n%s", out)
	}
	// One commit on each side, paired: a plain diff of the two heads
	// would instead show b.txt created from nothing all over again.
	if strings.Count(out, "add b") != 1 {
		t.Fatalf("range-diff lists the commit more than once:\n%s", out)
	}

	// Naming revisions explicitly, and refusing one that is not a
	// revision of this merge request.
	if _, _, code := inst.ssh(t, key, "", "mr", "range-diff", "alice/app", "1",
		"--from", revs[0].SHA, "--to", revs[1].SHA); code != 0 {
		t.Fatal("explicit --from/--to failed")
	}
	if _, errOut, code := inst.ssh(t, key, "", "mr", "range-diff", "alice/app", "1",
		"--from", "0123456789ab"); code != 3 || !strings.Contains(errOut, "not a revision") {
		t.Fatalf("unknown revision: exit %d, %s", code, errOut)
	}
	if _, _, code := inst.ssh(t, key, "", "mr", "range-diff", "alice/app", "1",
		"--from", revs[1].SHA, "--to", revs[1].SHA); code != 2 {
		t.Fatal("comparing a revision with itself was accepted")
	}

	// A push that changes nothing does not add a revision.
	mustGit(t, dir, env, "push", "-q", "--force", "origin", "feat")
	if got := revisions(t, inst, key); len(got) != 2 {
		t.Fatalf("a no-op push added a revision: %d", len(got))
	}
}

type revision struct {
	N       int    `json:"n"`
	SHA     string `json:"sha"`
	Current bool   `json:"current"`
}

func revisions(t *testing.T, inst *instance, key string) []revision {
	t.Helper()
	out, errOut, code := inst.ssh(t, key, "", "mr", "revisions", "alice/app", "1", "--json")
	if code != 0 {
		t.Fatalf("mr revisions: %s", errOut)
	}
	var env struct {
		Data []revision `json:"data"`
	}
	json.Unmarshal([]byte(out), &env)
	return env.Data
}
