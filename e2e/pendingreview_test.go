package e2e

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestPendingReviewBatch is #111's second stage: a reviewer composes a
// review and submits it as a unit, instead of every comment landing in
// the author's inbox the moment it is typed.
func TestPendingReviewBatch(t *testing.T) {
	inst := startInstance(t)
	authorKey := inst.newKey(t, "author")
	reviewerKey := inst.newKey(t, "reviewer")
	inst.admin(t, "admin", "user", "create", "author", "--key", authorKey+".pub")
	inst.admin(t, "admin", "user", "create", "reviewer", "--key", reviewerKey+".pub")
	if _, errOut, code := inst.ssh(t, authorKey, "", "repo", "create", "author/lib"); code != 0 {
		t.Fatalf("repo create: %s", errOut)
	}
	if _, _, code := inst.ssh(t, authorKey, "", "repo", "access", "grant", "author/lib", "reviewer", "write"); code != 0 {
		t.Fatal("grant failed")
	}
	if _, _, code := inst.ssh(t, authorKey, "", "repo", "settings", "require-resolved", "author/lib", "on"); code != 0 {
		t.Fatal("require-resolved failed")
	}

	env := inst.gitEnv(authorKey)
	work := t.TempDir()
	mustGit(t, work, env, "clone", inst.sshURL("author/lib"), "w")
	dir := filepath.Join(work, "w")
	os.WriteFile(filepath.Join(dir, "a.go"), []byte("package lib\n"), 0o644)
	mustGit(t, dir, env, "checkout", "-q", "-b", "main")
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, env, "commit", "-q", "-m", "base")
	mustGit(t, dir, env, "push", "-q", "origin", "main")
	mustGit(t, dir, env, "checkout", "-q", "-b", "feat")
	os.WriteFile(filepath.Join(dir, "a.go"), []byte("package lib\n\nvar A = 1\nvar B = 2\n"), 0o644)
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, env, "commit", "-q", "-m", "add A and B")
	mustGit(t, dir, env, "push", "-q", "origin", "feat")
	if _, errOut, code := inst.ssh(t, authorKey, "", "mr", "create", "author/lib",
		"--source", "feat", "--target", "main", "--title", "'add vars'"); code != 0 {
		t.Fatalf("mr create: %s", errOut)
	}

	// Two comments held back.
	for _, line := range []string{"3", "4"} {
		if _, errOut, code := inst.ssh(t, reviewerKey, "", "mr", "diff-comment", "author/lib", "1",
			"--path", "a.go", "--line", line, "--pending", "--message", "'name it better'"); code != 0 {
			t.Fatalf("pending comment on line %s: %s", line, errOut)
		}
	}

	// The author sees none of it, and their inbox is untouched.
	if out := threadsFor(t, inst, authorKey, "1"); len(out) != 0 {
		t.Fatalf("author sees unsubmitted comments: %v", out)
	}
	if got := inbox(t, inst, authorKey); strings.Contains(got, "commented on") {
		t.Fatalf("an unsubmitted comment reached the author's inbox:\n%s", got)
	}
	// The reviewer sees their own.
	if out := threadsFor(t, inst, reviewerKey, "1"); len(out) != 2 {
		t.Fatalf("reviewer sees %d of their own pending threads, want 2", len(out))
	}

	// And an unsubmitted thread must not gate the merge: nobody else can
	// see it, so nobody else could resolve it.
	if _, errOut, code := inst.ssh(t, authorKey, "", "mr", "merge", "author/lib", "1"); code != 0 {
		t.Fatalf("pending thread blocked a merge: %s", errOut)
	}

	// Same again on a second MR, this time submitted.
	mustGit(t, dir, env, "checkout", "-q", "-b", "feat2", "main")
	os.WriteFile(filepath.Join(dir, "b.go"), []byte("package lib\n\nvar C = 3\n"), 0o644)
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, env, "commit", "-q", "-m", "add C")
	mustGit(t, dir, env, "push", "-q", "origin", "feat2")
	if _, errOut, code := inst.ssh(t, authorKey, "", "mr", "create", "author/lib",
		"--source", "feat2", "--target", "main", "--title", "'add C'"); code != 0 {
		t.Fatalf("mr create: %s", errOut)
	}
	if _, errOut, code := inst.ssh(t, reviewerKey, "", "mr", "diff-comment", "author/lib", "2",
		"--path", "b.go", "--line", "3", "--pending", "--message", "'C needs a doc comment'"); code != 0 {
		t.Fatalf("pending comment: %s", errOut)
	}
	out, errOut, code := inst.ssh(t, reviewerKey, "", "mr", "review", "author/lib", "2", "--request-changes", "--json")
	if code != 0 {
		t.Fatalf("review: %s", errOut)
	}
	if !strings.Contains(out, `"published":1`) {
		t.Fatalf("review did not publish the batch: %s", out)
	}
	if n := len(threadsFor(t, inst, authorKey, "2")); n != 1 {
		t.Fatalf("author sees %d threads after the review, want 1", n)
	}
	// One notification for the review, carrying the count — not one per
	// comment as they were written.
	got := inbox(t, inst, authorKey)
	if !strings.Contains(got, "with 1 comment") {
		t.Fatalf("review notification does not mention the batch:\n%s", got)
	}
	// Now it gates.
	if _, errOut, code := inst.ssh(t, authorKey, "", "mr", "merge", "author/lib", "2"); code != 4 ||
		!strings.Contains(errOut, "threads resolved") {
		t.Fatalf("published thread did not gate: exit %d, %s", code, errOut)
	}

	// Discard throws away only what has not been submitted.
	if _, errOut, code := inst.ssh(t, reviewerKey, "", "mr", "diff-comment", "author/lib", "2",
		"--path", "b.go", "--line", "1", "--pending", "--message", "'never mind'"); code != 0 {
		t.Fatalf("pending comment: %s", errOut)
	}
	out, errOut, code = inst.ssh(t, reviewerKey, "", "mr", "review", "author/lib", "2", "--discard", "--json")
	if code != 0 || !strings.Contains(out, `"discarded":1`) {
		t.Fatalf("discard: exit %d, %s, %s", code, errOut, out)
	}
	if n := len(threadsFor(t, inst, reviewerKey, "2")); n != 1 {
		t.Fatalf("discard removed a published comment: %d threads remain", n)
	}
	// A verdict and a discard together is a usage error, not a guess.
	if _, _, code := inst.ssh(t, reviewerKey, "", "mr", "review", "author/lib", "2", "--approve", "--discard"); code != 2 {
		t.Fatalf("--approve --discard exit %d, want 2", code)
	}
}

// threadsFor returns the diff-comment thread ids this account can see on
// one merge request. Pending threads belong to their author alone, so who
// asks changes the answer — which is the whole point.
func threadsFor(t *testing.T, inst *instance, key, n string) []int64 {
	t.Helper()
	out, errOut, code := inst.ssh(t, key, "", "mr", "threads", "author/lib", n, "--json")
	if code != 0 {
		t.Fatalf("mr threads: %s", errOut)
	}
	var env struct {
		Data []struct {
			ID int64 `json:"id"`
		} `json:"data"`
	}
	json.Unmarshal([]byte(out), &env)
	var ids []int64
	for _, d := range env.Data {
		ids = append(ids, d.ID)
	}
	return ids
}
