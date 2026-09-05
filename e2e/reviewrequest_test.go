package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestMRReviewRequest drives "mr review request" (#145) end to end: a
// requested reviewer reaches the queue without being otherwise involved,
// drops out once they review the current head, comes back on a new push,
// and --remove takes them out outright. A separate, private repository
// checks that requesting someone who cannot read it is refused.
func TestMRReviewRequest(t *testing.T) {
	inst := startInstance(t)
	aliceKey := inst.newKey(t, "alice")
	bobKey := inst.newKey(t, "bob")
	carolKey := inst.newKey(t, "carol")
	inst.admin(t, "admin", "user", "create", "alice", "--key", aliceKey+".pub")
	inst.admin(t, "admin", "user", "create", "bob", "--key", bobKey+".pub")
	inst.admin(t, "admin", "user", "create", "carol", "--key", carolKey+".pub")

	// Public, and bob is granted nothing: any involvement he has in the
	// queue can only come from being asked directly.
	if _, errOut, code := inst.ssh(t, aliceKey, "", "repo", "create", "alice/app"); code != 0 {
		t.Fatalf("repo create: %s", errOut)
	}

	env := inst.gitEnv(aliceKey)
	work := t.TempDir()
	mustGit(t, work, env, "clone", inst.sshURL("alice/app"), "w")
	dir := filepath.Join(work, "w")
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a\n"), 0o644)
	mustGit(t, dir, env, "checkout", "-q", "-b", "main")
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, env, "commit", "-q", "-m", "base")
	mustGit(t, dir, env, "push", "-q", "origin", "main")
	mustGit(t, dir, env, "checkout", "-q", "-b", "feat")
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a\nb\n"), 0o644)
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, env, "commit", "-q", "-m", "feat")
	mustGit(t, dir, env, "push", "-q", "origin", "feat")

	if _, errOut, code := inst.ssh(t, aliceKey, "", "mr", "create", "alice/app",
		"--source", "feat", "--target", "main", "--title", "'add b'", "--draft"); code != 0 {
		t.Fatalf("mr create: %s", errOut)
	}

	// Asking while a draft does not put it in bob's queue: draft merge
	// requests stay out of the review queue for everyone, requested or
	// not.
	if _, errOut, code := inst.ssh(t, aliceKey, "", "mr", "review", "request", "alice/app", "1", "--add", "bob"); code != 0 {
		t.Fatalf("review request: %s", errOut)
	}
	if q := reviewQueue(t, inst, bobKey); len(q) != 0 {
		t.Fatalf("a draft is waiting on the requested reviewer: %v", q)
	}

	// Ready: the request now surfaces, and it notified bob at the same
	// moment it notified everyone else.
	if _, errOut, code := inst.ssh(t, aliceKey, "", "mr", "ready", "alice/app", "1"); code != 0 {
		t.Fatalf("mr ready: %s", errOut)
	}
	if !strings.Contains(inbox(t, inst, bobKey), "ready for review") {
		t.Fatalf("requested reviewer not notified by mr ready:\n%s", inbox(t, inst, bobKey))
	}
	if q := reviewQueue(t, inst, bobKey); len(q) != 1 || q[0] != 1 {
		t.Fatalf("requested reviewer not in queue: %v", q)
	}

	// Reviewing the current head empties the queue, the same rule an
	// involved reviewer follows.
	if _, errOut, code := inst.ssh(t, bobKey, "", "mr", "review", "alice/app", "1", "--approve"); code != 0 {
		t.Fatalf("review: %s", errOut)
	}
	if q := reviewQueue(t, inst, bobKey); len(q) != 0 {
		t.Fatalf("queue did not empty after reviewing the head: %v", q)
	}

	// A new push moves the head, so the review no longer covers it: back
	// in the queue.
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a\nb\nc\n"), 0o644)
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, env, "commit", "-q", "-m", "more")
	mustGit(t, dir, env, "push", "-q", "origin", "feat")
	if q := reviewQueue(t, inst, bobKey); len(q) != 1 || q[0] != 1 {
		t.Fatalf("new head did not bring the request back: %v", q)
	}

	// --remove takes it out outright.
	if _, errOut, code := inst.ssh(t, aliceKey, "", "mr", "review", "request", "alice/app", "1", "--remove", "bob"); code != 0 {
		t.Fatalf("review request --remove: %s", errOut)
	}
	if q := reviewQueue(t, inst, bobKey); len(q) != 0 {
		t.Fatalf("queue after --remove: %v", q)
	}
	if _, _, code := inst.ssh(t, aliceKey, "", "mr", "review", "request", "alice/app", "1", "--remove", "bob"); code != 3 {
		t.Fatalf("removing an absent reviewer should 404, got %d", code)
	}

	// A private repository where carol has no access at all: asking her
	// for a review is refused, not silently recorded.
	if _, errOut, code := inst.ssh(t, aliceKey, "", "repo", "create", "alice/secret", "--private"); code != 0 {
		t.Fatalf("repo create: %s", errOut)
	}
	work2 := t.TempDir()
	mustGit(t, work2, env, "clone", inst.sshURL("alice/secret"), "w")
	dir2 := filepath.Join(work2, "w")
	os.WriteFile(filepath.Join(dir2, "a.txt"), []byte("a\n"), 0o644)
	mustGit(t, dir2, env, "checkout", "-q", "-b", "main")
	mustGit(t, dir2, env, "add", ".")
	mustGit(t, dir2, env, "commit", "-q", "-m", "base")
	mustGit(t, dir2, env, "push", "-q", "origin", "main")
	mustGit(t, dir2, env, "checkout", "-q", "-b", "feat")
	os.WriteFile(filepath.Join(dir2, "a.txt"), []byte("a\nb\n"), 0o644)
	mustGit(t, dir2, env, "add", ".")
	mustGit(t, dir2, env, "commit", "-q", "-m", "feat")
	mustGit(t, dir2, env, "push", "-q", "origin", "feat")
	if _, errOut, code := inst.ssh(t, aliceKey, "", "mr", "create", "alice/secret",
		"--source", "feat", "--target", "main", "--title", "'private'"); code != 0 {
		t.Fatalf("mr create: %s", errOut)
	}
	_, errOut, code := inst.ssh(t, aliceKey, "", "mr", "review", "request", "alice/secret", "1", "--add", "carol")
	if code != 4 || !strings.Contains(errOut, "cannot read") {
		t.Fatalf("requesting a review from someone with no access should be refused: exit %d, %s", code, errOut)
	}
}
