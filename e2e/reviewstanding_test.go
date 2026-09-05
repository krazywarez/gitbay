package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestReviewsCountOnlyFromWriters: a merge gate is a repository's own
// rule, so only people the repository trusts can decide it.
//
// mr review resolves with CanRead and applies no further check, and
// reviewGates counted every fresh verdict. On a public repository that
// let anyone with an account satisfy require-approvals — defeating
// four-eyes review by having two accounts, which on an open-registration
// instance means defeating it outright — and equally let them block a
// merge the owner wanted (#147).
//
// Reviewing stays open to everyone: an outside opinion on a public change
// is worth having. It just does not decide the gate.
func TestReviewsCountOnlyFromWriters(t *testing.T) {
	inst := startInstance(t)
	ownerKey := inst.newKey(t, "owner")
	writerKey := inst.newKey(t, "writer")
	strangerKey := inst.newKey(t, "stranger")
	inst.admin(t, "admin", "user", "create", "owner", "--key", ownerKey+".pub",
		"--email", "owner@example.test", "--verified")
	inst.admin(t, "admin", "user", "create", "writer", "--key", writerKey+".pub")
	inst.admin(t, "admin", "user", "create", "stranger", "--key", strangerKey+".pub")

	// Public, so the stranger can read it and open a review at all.
	if _, errOut, code := inst.ssh(t, ownerKey, "", "repo", "create", "owner/pub"); code != 0 {
		t.Fatalf("repo create: %s", errOut)
	}
	if _, _, code := inst.ssh(t, ownerKey, "", "repo", "access", "grant", "owner/pub", "writer", "write"); code != 0 {
		t.Fatal("grant failed")
	}
	if _, _, code := inst.ssh(t, ownerKey, "", "repo", "settings", "require-approvals", "owner/pub", "1"); code != 0 {
		t.Fatal("require-approvals failed")
	}

	env := inst.gitEnv(ownerKey)
	work := t.TempDir()
	mustGit(t, work, env, "clone", inst.sshURL("owner/pub"), "w")
	dir := filepath.Join(work, "w")
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a\n"), 0o644)
	mustGit(t, dir, env, "checkout", "-q", "-b", "main")
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, env, "commit", "-q", "-m", "base")
	mustGit(t, dir, env, "push", "-q", "origin", "main")
	newMR := func(branch string) {
		t.Helper()
		mustGit(t, dir, env, "checkout", "-q", "-b", branch, "main")
		mustGit(t, dir, env, "commit", "-q", "--allow-empty", "-m", branch)
		mustGit(t, dir, env, "push", "-q", "origin", branch)
		if _, errOut, code := inst.ssh(t, ownerKey, "", "mr", "create", "owner/pub",
			"--source", branch, "--target", "main", "--title", "'"+branch+"'"); code != 0 {
			t.Fatalf("mr create %s: %s", branch, errOut)
		}
	}

	// !1 — a stranger's approval must not satisfy the gate.
	newMR("feat1")
	if _, errOut, code := inst.ssh(t, strangerKey, "", "mr", "review", "owner/pub", "1", "--approve"); code != 0 {
		t.Fatalf("a stranger should still be able to review a public MR: %s", errOut)
	}
	_, errOut, code := inst.ssh(t, ownerKey, "", "mr", "merge", "owner/pub", "1")
	if code != 4 || !strings.Contains(errOut, "fresh approval") {
		t.Fatalf("a stranger's approval satisfied require-approvals: exit %d, %s", code, errOut)
	}
	// A writer's approval does.
	if _, errOut, code := inst.ssh(t, writerKey, "", "mr", "review", "owner/pub", "1", "--approve"); code != 0 {
		t.Fatalf("writer review: %s", errOut)
	}
	if _, errOut, code := inst.ssh(t, ownerKey, "", "mr", "merge", "owner/pub", "1"); code != 0 {
		t.Fatalf("a writer's approval did not satisfy the gate: %s", errOut)
	}

	// !2 — a stranger's objection must not block the owner.
	newMR("feat2")
	if _, _, code := inst.ssh(t, writerKey, "", "mr", "review", "owner/pub", "2", "--approve"); code != 0 {
		t.Fatal("writer approve failed")
	}
	if _, errOut, code := inst.ssh(t, strangerKey, "", "mr", "review", "owner/pub", "2", "--request-changes"); code != 0 {
		t.Fatalf("stranger request-changes: %s", errOut)
	}
	if _, errOut, code := inst.ssh(t, ownerKey, "", "mr", "merge", "owner/pub", "2"); code != 0 {
		t.Fatalf("a stranger blocked the owner's merge: %s", errOut)
	}

	// !3 — a writer's objection still blocks, or the gate means nothing.
	newMR("feat3")
	if _, _, code := inst.ssh(t, writerKey, "", "mr", "review", "owner/pub", "3", "--request-changes"); code != 0 {
		t.Fatal("writer request-changes failed")
	}
	_, errOut, code = inst.ssh(t, ownerKey, "", "mr", "merge", "owner/pub", "3")
	if code != 4 || !strings.Contains(errOut, "writer requested changes") {
		t.Fatalf("a writer's objection did not block: exit %d, %s", code, errOut)
	}

	// The review is still recorded and visible either way — it is the
	// gate that ignores it, not the conversation.
	out, _, _ := inst.ssh(t, ownerKey, "", "mr", "show", "owner/pub", "2", "--json")
	if !strings.Contains(out, "stranger") {
		t.Fatalf("the stranger's review was discarded rather than recorded:\n%s", out)
	}
	if !strings.Contains(out, `"counts":false`) {
		t.Fatalf("mr show does not say the review is not counted:\n%s", out)
	}
}
