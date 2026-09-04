package e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestTwoAccountReviewLoop drives the collaboration features end to end as
// two distinct accounts: the author never approves their own work, and the
// reviewer sees each step arrive.
//
// This is not the second human #139 asks for, and it is worth being exact
// about why. The CODEOWNERS bug (#99) was a gate that never fired; a test
// written from the same understanding as the code would have asserted the
// same wrong thing. What this does close is the narrower gap that nothing
// exercised the loop end to end at all — every feature had its own test
// and none of them met.
func TestTwoAccountReviewLoop(t *testing.T) {
	inst := startInstance(t)
	authorKey := inst.newKey(t, "author")
	reviewerKey := inst.newKey(t, "reviewer")
	// The author merges, and a merge commit carries their identity, so
	// the account needs a verified address. The first merge below can
	// fast-forward and would not have needed one; the second cannot.
	inst.admin(t, "admin", "user", "create", "author", "--key", authorKey+".pub",
		"--email", "author@example.test", "--verified")
	inst.admin(t, "admin", "user", "create", "reviewer", "--key", reviewerKey+".pub")
	// A third account with write access who owns nothing, so an approval
	// from them satisfies the count and leaves CODEOWNERS unsatisfied.
	helperKey := inst.newKey(t, "helper")
	inst.admin(t, "admin", "user", "create", "helper", "--key", helperKey+".pub")

	if _, errOut, code := inst.ssh(t, authorKey, "", "repo", "create", "author/lib"); code != 0 {
		t.Fatalf("repo create: %s", errOut)
	}
	for _, who := range []string{"reviewer", "helper"} {
		if _, _, code := inst.ssh(t, authorKey, "", "repo", "access", "grant", "author/lib", who, "write"); code != 0 {
			t.Fatalf("grant %s failed", who)
		}
	}
	// Every gate at once, which is how a repository that means it is set
	// up, and which no single-feature test covers together.
	for _, s := range [][]string{
		{"repo", "settings", "require-approvals", "author/lib", "1"},
		{"repo", "settings", "require-resolved", "author/lib", "on"},
		{"repo", "settings", "require-codeowners", "author/lib", "on"},
	} {
		if _, errOut, code := inst.ssh(t, authorKey, "", s...); code != 0 {
			t.Fatalf("%v: %s", s, errOut)
		}
	}

	env := inst.gitEnv(authorKey)
	work := t.TempDir()
	mustGit(t, work, env, "clone", inst.sshURL("author/lib"), "w")
	dir := filepath.Join(work, "w")
	os.WriteFile(filepath.Join(dir, "CODEOWNERS"), []byte("*.go @reviewer\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "lib.go"), []byte("package lib\n"), 0o644)
	mustGit(t, dir, env, "checkout", "-q", "-b", "main")
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, env, "commit", "-q", "-m", "base")
	mustGit(t, dir, env, "push", "-q", "origin", "main")

	mustGit(t, dir, env, "checkout", "-q", "-b", "feat")
	os.WriteFile(filepath.Join(dir, "lib.go"), []byte("package lib\n\nvar V = 1\n"), 0o644)
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, env, "commit", "-q", "-m", "add V")
	mustGit(t, dir, env, "push", "-q", "origin", "feat")

	// Opened as a draft: the reviewer is not asked yet.
	if _, errOut, code := inst.ssh(t, authorKey, "", "mr", "create", "author/lib",
		"--source", "feat", "--target", "main", "--title", "'add V'", "--draft"); code != 0 {
		t.Fatalf("mr create: %s", errOut)
	}
	if q := reviewQueue(t, inst, reviewerKey); len(q) != 0 {
		t.Fatalf("a draft is waiting on the reviewer: %v", q)
	}

	// Ready. Now it is theirs, and it reaches their inbox.
	if _, errOut, code := inst.ssh(t, authorKey, "", "mr", "ready", "author/lib", "1"); code != 0 {
		t.Fatalf("mr ready: %s", errOut)
	}
	if q := reviewQueue(t, inst, reviewerKey); len(q) != 1 || q[0] != 1 {
		t.Fatalf("review queue = %v, want !1", q)
	}
	// The queue is how a reviewer finds out, and the only way: there is
	// no "request review from <user>", so nothing is pushed to someone
	// who is neither an owner nor already in the thread. Writing this
	// test is what surfaced that — see #145.
	if got := inbox(t, inst, reviewerKey); strings.Contains(got, "ready for review") {
		t.Fatalf("a reviewer is notified after all; #145 and this comment are stale:\n%s", got)
	}

	// The author cannot approve their own work past the gate.
	if _, _, code := inst.ssh(t, authorKey, "", "mr", "review", "author/lib", "1", "--approve"); code != 0 {
		t.Fatal("self review refused outright; it should be recorded and not counted")
	}
	_, errOut, code := inst.ssh(t, authorKey, "", "mr", "merge", "author/lib", "1")
	if code != 4 || !strings.Contains(errOut, "requires 1 fresh approval") {
		t.Fatalf("author's own approval counted: exit %d, %s", code, errOut)
	}

	// A review thread from the reviewer blocks on require-resolved.
	out, errOut, code := inst.ssh(t, reviewerKey, "", "mr", "diff-comment", "author/lib", "1",
		"--path", "lib.go", "--line", "3", "--message", "'name it better'", "--json")
	if code != 0 {
		t.Fatalf("diff-comment: %s", errOut)
	}
	var thread struct {
		Data struct {
			Thread int64 `json:"thread"`
		} `json:"data"`
	}
	json.Unmarshal([]byte(out), &thread)
	if !strings.Contains(inbox(t, inst, authorKey), "commented on lib.go") {
		t.Fatalf("author not told about the review thread:\n%s", inbox(t, inst, authorKey))
	}

	if _, _, code := inst.ssh(t, reviewerKey, "", "mr", "review", "author/lib", "1", "--approve"); code != 0 {
		t.Fatal("reviewer approve failed")
	}
	_, errOut, code = inst.ssh(t, authorKey, "", "mr", "merge", "author/lib", "1")
	if code != 4 || !strings.Contains(errOut, "threads resolved") {
		t.Fatalf("open thread did not block the merge: exit %d, %s", code, errOut)
	}

	// Resolve, and every gate is satisfied at once: an owner approved, the
	// count is met, the thread is closed.
	if _, errOut, code := inst.ssh(t, reviewerKey, "", "mr", "resolve", "author/lib", "1",
		fmt.Sprint(thread.Data.Thread)); code != 0 {
		t.Fatalf("resolve: %s", errOut)
	}
	if _, errOut, code := inst.ssh(t, authorKey, "", "mr", "merge", "author/lib", "1"); code != 0 {
		t.Fatalf("fully reviewed MR refused: %s", errOut)
	}
	if !strings.Contains(inbox(t, inst, reviewerKey), "merged !1") {
		t.Fatalf("reviewer not told about the merge:\n%s", inbox(t, inst, reviewerKey))
	}

	// A CODEOWNERS gate with no owner approval still refuses, on a second
	// MR, so the pass above was the approval and not the gate being off.
	mustGit(t, dir, env, "checkout", "-q", "-b", "feat2", "main")
	os.WriteFile(filepath.Join(dir, "other.go"), []byte("package lib\n"), 0o644)
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, env, "commit", "-q", "-m", "another")
	mustGit(t, dir, env, "push", "-q", "origin", "feat2")
	if _, errOut, code := inst.ssh(t, authorKey, "", "mr", "create", "author/lib",
		"--source", "feat2", "--target", "main", "--title", "'another'"); code != 0 {
		t.Fatalf("mr create: %s", errOut)
	}
	// helper's approval meets require-approvals but owns none of the
	// changed files, so what refuses this merge is CODEOWNERS alone —
	// the gate that used to be reachable only through the approval count
	// (#99), now on its own toggle (#142).
	if _, _, code := inst.ssh(t, helperKey, "", "mr", "review", "author/lib", "2", "--approve"); code != 0 {
		t.Fatal("helper approve failed")
	}
	_, errOut, code = inst.ssh(t, authorKey, "", "mr", "merge", "author/lib", "2")
	if code != 4 || !strings.Contains(errOut, "CODEOWNERS") {
		t.Fatalf("CODEOWNERS gate did not fire on the second MR: exit %d, %s", code, errOut)
	}
	// And the owner's approval clears it.
	if _, _, code := inst.ssh(t, reviewerKey, "", "mr", "review", "author/lib", "2", "--approve"); code != 0 {
		t.Fatal("reviewer approve failed")
	}
	if _, errOut, code := inst.ssh(t, authorKey, "", "mr", "merge", "author/lib", "2"); code != 0 {
		t.Fatalf("owner-approved MR refused: %s", errOut)
	}
}

// reviewQueue returns the MR numbers waiting on this account.
func reviewQueue(t *testing.T, inst *instance, key string) []int64 {
	t.Helper()
	out, errOut, code := inst.ssh(t, key, "", "dashboard", "--json")
	if code != 0 {
		t.Fatalf("dashboard: %s", errOut)
	}
	var env struct {
		Data struct {
			Reviews []struct {
				Number int64 `json:"number"`
			} `json:"review_queue"`
		} `json:"data"`
	}
	json.Unmarshal([]byte(out), &env)
	var ns []int64
	for _, r := range env.Data.Reviews {
		ns = append(ns, r.Number)
	}
	return ns
}

// inbox returns this account's notifications as raw JSON.
func inbox(t *testing.T, inst *instance, key string) string {
	t.Helper()
	out, errOut, code := inst.ssh(t, key, "", "notifications", "list", "--all", "--json")
	if code != 0 {
		t.Fatalf("notifications list: %s", errOut)
	}
	return out
}
