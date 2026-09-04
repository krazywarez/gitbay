package e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMergeRequirements(t *testing.T) {
	inst := startInstance(t)
	aliceKey := inst.newKey(t, "alice")
	bobKey := inst.newKey(t, "bob")
	carolKey := inst.newKey(t, "carol")
	inst.admin(t, "admin", "user", "create", "alice",
		"--key", aliceKey+".pub", "--email", "alice@example.test", "--verified")
	inst.admin(t, "admin", "user", "create", "bob", "--key", bobKey+".pub")
	inst.admin(t, "admin", "user", "create", "carol", "--key", carolKey+".pub")

	// Repo with CODEOWNERS on main: carol owns *.go.
	if _, errOut, code := inst.ssh(t, aliceKey, "", "repo", "create", "alice/svc"); code != 0 {
		t.Fatalf("repo create: %s", errOut)
	}
	for _, u := range []string{"bob", "carol"} {
		if _, _, code := inst.ssh(t, aliceKey, "", "repo", "access", "grant", "alice/svc", u, "write"); code != 0 {
			t.Fatal("grant failed")
		}
	}
	work := t.TempDir()
	env := inst.gitEnv(aliceKey)
	mustGit(t, work, env, "clone", inst.sshURL("alice/svc"), "w")
	dir := filepath.Join(work, "w")
	os.WriteFile(filepath.Join(dir, "CODEOWNERS"), []byte("*.go @carol\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "svc.go"), []byte("package svc\n"), 0o644)
	mustGit(t, dir, env, "checkout", "-q", "-b", "main")
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, env, "commit", "-q", "-m", "base")
	mustGit(t, dir, env, "push", "-q", "origin", "main")

	// MR by alice touching a .go file.
	mustGit(t, dir, env, "checkout", "-q", "-b", "feat")
	os.WriteFile(filepath.Join(dir, "svc.go"), []byte("package svc\n\nvar V = 1\n"), 0o644)
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, env, "commit", "-q", "-m", "change")
	mustGit(t, dir, env, "push", "-q", "origin", "feat")
	if _, errOut, code := inst.ssh(t, aliceKey, "", "mr", "create", "alice/svc",
		"--source", "feat", "--target", "main", "--title", "'change'"); code != 0 {
		t.Fatalf("mr create: %s", errOut)
	}
	if _, _, code := inst.ssh(t, aliceKey, "", "repo", "settings", "require-approvals", "alice/svc", "1"); code != 0 {
		t.Fatal("require-approvals failed")
	}
	if _, _, code := inst.ssh(t, aliceKey, "", "repo", "settings", "require-codeowners", "alice/svc", "on"); code != 0 {
		t.Fatal("require-codeowners failed")
	}

	// No approvals: refused. The author's own approval does not count.
	_, errOut, code := inst.ssh(t, aliceKey, "", "mr", "merge", "alice/svc", "1")
	if code != 4 || !strings.Contains(errOut, "requires 1 fresh approval") {
		t.Fatalf("no-approval merge: exit %d, %s", code, errOut)
	}
	if _, _, code = inst.ssh(t, aliceKey, "", "mr", "review", "alice/svc", "1", "--approve"); code != 0 {
		t.Fatal("self review failed")
	}
	if _, _, code = inst.ssh(t, aliceKey, "", "mr", "merge", "alice/svc", "1"); code != 4 {
		t.Fatal("author self-approval counted")
	}

	// Bob approves — but CODEOWNERS demands carol for *.go.
	if _, _, code = inst.ssh(t, bobKey, "", "mr", "review", "alice/svc", "1", "--approve"); code != 0 {
		t.Fatal("bob review failed")
	}
	_, errOut, code = inst.ssh(t, aliceKey, "", "mr", "merge", "alice/svc", "1")
	if code != 4 || !strings.Contains(errOut, "CODEOWNERS") || !strings.Contains(errOut, "carol") {
		t.Fatalf("codeowners gate: exit %d, %s", code, errOut)
	}

	// A fresh request-changes blocks even with approvals present.
	if _, _, code = inst.ssh(t, carolKey, "", "mr", "review", "alice/svc", "1", "--request-changes"); code != 0 {
		t.Fatal("carol review failed")
	}
	_, errOut, code = inst.ssh(t, aliceKey, "", "mr", "merge", "alice/svc", "1")
	if code != 4 || !strings.Contains(errOut, "carol requested changes") {
		t.Fatalf("request-changes block: exit %d, %s", code, errOut)
	}

	// Carol's latest review wins: her approval satisfies both the count
	// and CODEOWNERS.
	if _, _, code = inst.ssh(t, carolKey, "", "mr", "review", "alice/svc", "1", "--approve"); code != 0 {
		t.Fatal("carol approve failed")
	}

	// require-resolved: an open thread still blocks; resolving unblocks.
	if _, _, code = inst.ssh(t, aliceKey, "", "repo", "settings", "require-resolved", "alice/svc", "on"); code != 0 {
		t.Fatal("require-resolved failed")
	}
	tout, _, code2 := inst.ssh(t, bobKey, "", "mr", "diff-comment", "alice/svc", "1",
		"--path", "svc.go", "--line", "3", "--message", "'name it better'", "--json")
	if code2 != 0 {
		t.Fatal("diff-comment failed")
	}
	var tenv struct {
		Data struct {
			Thread int64 `json:"thread"`
		} `json:"data"`
	}
	json.Unmarshal([]byte(tout), &tenv)
	_, errOut, code = inst.ssh(t, aliceKey, "", "mr", "merge", "alice/svc", "1")
	if code != 4 || !strings.Contains(errOut, "threads resolved") {
		t.Fatalf("resolved gate: exit %d, %s", code, errOut)
	}
	if _, _, code = inst.ssh(t, bobKey, "", "mr", "resolve", "alice/svc", "1", fmt.Sprint(tenv.Data.Thread)); code != 0 {
		t.Fatal("resolve failed")
	}
	if _, errOut, code = inst.ssh(t, aliceKey, "", "mr", "merge", "alice/svc", "1"); code != 0 {
		t.Fatalf("fully gated merge: %s", errOut)
	}

	// Stale approvals never count: new MR, approve, force-push, refused.
	mustGit(t, dir, env, "fetch", "-q", "origin")
	mustGit(t, dir, env, "checkout", "-q", "-b", "feat2", "origin/main")
	os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("n\n"), 0o644)
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, env, "commit", "-q", "-m", "notes")
	mustGit(t, dir, env, "push", "-q", "origin", "feat2")
	if _, _, code := inst.ssh(t, aliceKey, "", "mr", "create", "alice/svc",
		"--source", "feat2", "--target", "main", "--title", "'notes'"); code != 0 {
		t.Fatal("mr2 create failed")
	}
	if _, _, code = inst.ssh(t, bobKey, "", "mr", "review", "alice/svc", "2", "--approve"); code != 0 {
		t.Fatal("bob approve 2 failed")
	}
	mustGit(t, dir, env, "commit", "-q", "--amend", "-m", "notes v2")
	mustGit(t, dir, env, "push", "-q", "--force", "origin", "feat2")
	_, errOut, code = inst.ssh(t, aliceKey, "", "mr", "merge", "alice/svc", "2")
	if code != 4 || !strings.Contains(errOut, "requires 1 fresh approval") {
		t.Fatalf("stale approval counted: exit %d, %s", code, errOut)
	}
}

// require_codeowners is the opt-in, not the file's presence: a repository
// can carry CODEOWNERS as documentation of who to ask without it gating
// merges. When it is on, it gates independently of require_approvals —
// the coupling that left owners unenforced under default settings (#99).
func TestCodeownersToggle(t *testing.T) {
	inst := startInstance(t)
	aliceKey := inst.newKey(t, "alice")
	carolKey := inst.newKey(t, "carol")
	inst.admin(t, "admin", "user", "create", "alice", "--key", aliceKey+".pub", "--email", "alice@example.test", "--verified")
	inst.admin(t, "admin", "user", "create", "carol", "--key", carolKey+".pub")
	if _, errOut, code := inst.ssh(t, aliceKey, "", "repo", "create", "alice/svc"); code != 0 {
		t.Fatalf("repo create: %s", errOut)
	}
	if _, _, code := inst.ssh(t, aliceKey, "", "repo", "access", "grant", "alice/svc", "carol", "write"); code != 0 {
		t.Fatal("grant failed")
	}
	work := t.TempDir()
	env := inst.gitEnv(aliceKey)
	mustGit(t, work, env, "clone", inst.sshURL("alice/svc"), "w")
	dir := filepath.Join(work, "w")
	os.WriteFile(filepath.Join(dir, "CODEOWNERS"), []byte("*.go @carol\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "svc.go"), []byte("package svc\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "lib.go"), []byte("package svc\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "README"), []byte("svc\n"), 0o644)
	mustGit(t, dir, env, "checkout", "-q", "-b", "main")
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, env, "commit", "-q", "-m", "base")
	mustGit(t, dir, env, "push", "-q", "origin", "main")

	// !1 and !3 touch owned files, !2 does not.
	for i, f := range []string{"svc.go", "README", "lib.go"} {
		mustGit(t, dir, env, "checkout", "-q", "-b", fmt.Sprintf("feat%d", i), "main")
		os.WriteFile(filepath.Join(dir, f), []byte("changed\n"), 0o644)
		mustGit(t, dir, env, "add", ".")
		mustGit(t, dir, env, "commit", "-q", "-m", "change")
		mustGit(t, dir, env, "push", "-q", "origin", fmt.Sprintf("feat%d", i))
		if _, errOut, code := inst.ssh(t, aliceKey, "", "mr", "create", "alice/svc",
			"--source", fmt.Sprintf("feat%d", i), "--target", "main", "--title", "'change'"); code != 0 {
			t.Fatalf("mr create: %s", errOut)
		}
	}

	// Toggle off, which is the default: the file is present and gates
	// nothing, so an owned file merges without its owner.
	if _, errOut, code := inst.ssh(t, aliceKey, "", "mr", "merge", "alice/svc", "3"); code != 0 {
		t.Fatalf("owned file gated with the toggle off: %s", errOut)
	}

	// Toggle on with require_approvals still 0: the owned file is gated,
	// the unowned one is not.
	if _, errOut, code := inst.ssh(t, aliceKey, "", "repo", "settings", "require-codeowners", "alice/svc", "on"); code != 0 {
		t.Fatalf("require-codeowners: %s", errOut)
	}
	_, errOut, code := inst.ssh(t, aliceKey, "", "mr", "merge", "alice/svc", "1")
	if code != 4 || !strings.Contains(errOut, "CODEOWNERS") || !strings.Contains(errOut, "carol") {
		t.Fatalf("codeowners gate with require-approvals off: exit %d, %s", code, errOut)
	}
	if _, errOut, code := inst.ssh(t, aliceKey, "", "mr", "merge", "alice/svc", "2"); code != 0 {
		t.Fatalf("unowned change refused: %s", errOut)
	}
	if _, _, code := inst.ssh(t, carolKey, "", "mr", "review", "alice/svc", "1", "--approve"); code != 0 {
		t.Fatal("carol review failed")
	}
	if _, errOut, code := inst.ssh(t, aliceKey, "", "mr", "merge", "alice/svc", "1"); code != 0 {
		t.Fatalf("owner-approved merge refused: %s", errOut)
	}

	// The toggle on a repository with no CODEOWNERS file says so rather
	// than silently gating nothing.
	if _, errOut, code := inst.ssh(t, aliceKey, "", "repo", "create", "alice/bare"); code != 0 {
		t.Fatalf("repo create: %s", errOut)
	}
	if _, _, code := inst.ssh(t, aliceKey, "", "repo", "settings", "require-codeowners", "alice/bare", "on"); code != 0 {
		t.Fatal("require-codeowners on alice/bare failed")
	}
	bare := t.TempDir()
	mustGit(t, bare, env, "clone", inst.sshURL("alice/bare"), "b")
	bdir := filepath.Join(bare, "b")
	os.WriteFile(filepath.Join(bdir, "a.txt"), []byte("a\n"), 0o644)
	mustGit(t, bdir, env, "checkout", "-q", "-b", "main")
	mustGit(t, bdir, env, "add", ".")
	mustGit(t, bdir, env, "commit", "-q", "-m", "base")
	mustGit(t, bdir, env, "push", "-q", "origin", "main")
	mustGit(t, bdir, env, "checkout", "-q", "-b", "feat")
	mustGit(t, bdir, env, "commit", "-q", "--allow-empty", "-m", "work")
	mustGit(t, bdir, env, "push", "-q", "origin", "feat")
	if _, errOut, code := inst.ssh(t, aliceKey, "", "mr", "create", "alice/bare",
		"--source", "feat", "--target", "main", "--title", "'work'"); code != 0 {
		t.Fatalf("mr create: %s", errOut)
	}
	_, errOut, code = inst.ssh(t, aliceKey, "", "mr", "merge", "alice/bare", "1")
	if code != 4 || !strings.Contains(errOut, "no CODEOWNERS file") {
		t.Fatalf("missing CODEOWNERS file: exit %d, %s", code, errOut)
	}
}
