package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSquashAndRebaseMerges(t *testing.T) {
	inst := startInstance(t)
	aliceKey := inst.newKey(t, "alice")
	bobKey := inst.newKey(t, "bob")
	inst.admin(t, "admin", "user", "create", "alice",
		"--key", aliceKey+".pub", "--email", "alice@example.test", "--verified")
	inst.admin(t, "admin", "user", "create", "bob",
		"--key", bobKey+".pub", "--email", "bob@example.test", "--verified")

	// Repo with bob granted write; bob authors branches, alice merges.
	if _, errOut, code := inst.ssh(t, aliceKey, "", "repo", "create", "alice/lib"); code != 0 {
		t.Fatalf("repo create: %s", errOut)
	}
	if _, _, code := inst.ssh(t, aliceKey, "", "repo", "access", "grant", "alice/lib", "bob", "write"); code != 0 {
		t.Fatal("grant failed")
	}

	aliceEnv := inst.gitEnv(aliceKey)
	bobEnv := inst.gitEnv(bobKey)
	work := t.TempDir()
	mustGit(t, work, aliceEnv, "clone", inst.sshURL("alice/lib"), "w")
	dir := filepath.Join(work, "w")
	os.WriteFile(filepath.Join(dir, "base.txt"), []byte("base\n"), 0o644)
	mustGit(t, dir, aliceEnv, "checkout", "-q", "-b", "main")
	mustGit(t, dir, aliceEnv, "add", ".")
	mustGit(t, dir, aliceEnv, "commit", "-q", "-m", "base")
	mustGit(t, dir, aliceEnv, "push", "-q", "origin", "main")

	// --- squash: two bob commits, diverged target -> one new commit ---
	bobWork := t.TempDir()
	mustGit(t, bobWork, bobEnv, "clone", inst.sshURL("alice/lib"), "w")
	bobDir := filepath.Join(bobWork, "w")
	mustGit(t, bobDir, bobEnv, "checkout", "-q", "-b", "feat1", "origin/main")
	os.WriteFile(filepath.Join(bobDir, "a.txt"), []byte("a\n"), 0o644)
	mustGit(t, bobDir, bobEnv, "add", ".")
	mustGit(t, bobDir, bobEnv, "commit", "-q", "-m", "wip 1")
	os.WriteFile(filepath.Join(bobDir, "b.txt"), []byte("b\n"), 0o644)
	mustGit(t, bobDir, bobEnv, "add", ".")
	mustGit(t, bobDir, bobEnv, "commit", "-q", "-m", "wip 2")
	mustGit(t, bobDir, bobEnv, "push", "-q", "origin", "feat1")
	if _, errOut, code := inst.ssh(t, bobKey, "", "mr", "create", "alice/lib",
		"--source", "feat1", "--target", "main", "--title", "'squash me'", "--body", "'two wips'"); code != 0 {
		t.Fatalf("mr create: %s", errOut)
	}
	// Target advances so ff is impossible.
	mustGit(t, dir, aliceEnv, "commit", "-q", "--allow-empty", "-m", "mainline")
	mustGit(t, dir, aliceEnv, "push", "-q", "origin", "main")

	before := strings.TrimSpace(mustGit(t, dir, aliceEnv, "rev-parse", "origin/main"))
	out, errOut, code := inst.ssh(t, aliceKey, "", "mr", "merge", "alice/lib", "1", "--strategy", "squash", "--json")
	if code != 0 {
		t.Fatalf("squash merge: %s", errOut)
	}
	if !strings.Contains(out, `"strategy":"squash"`) {
		t.Fatalf("squash output: %s", out)
	}
	mustGit(t, dir, aliceEnv, "fetch", "-q", "origin")
	// Exactly one commit landed on top of the old tip.
	count := strings.TrimSpace(mustGit(t, dir, aliceEnv, "rev-list", "--count", before+"..origin/main"))
	if count != "1" {
		t.Fatalf("squash added %s commits, want 1", count)
	}
	// Single parent, author = MR author (bob), committer = merger (alice).
	ident := strings.TrimSpace(mustGit(t, dir, aliceEnv, "log", "-1",
		"--format=%an <%ae>|%cn <%ce>|%p|%s", "origin/main"))
	parts := strings.Split(ident, "|")
	if parts[0] != "bob <bob@example.test>" || parts[1] != "alice <alice@example.test>" {
		t.Fatalf("squash identities: %s", ident)
	}
	if strings.Contains(parts[2], " ") {
		t.Fatalf("squash commit has multiple parents: %s", ident)
	}
	if parts[3] != "squash me (!1)" {
		t.Fatalf("squash subject: %s", ident)
	}
	// Both files present.
	mustGit(t, dir, aliceEnv, "checkout", "-q", "main")
	mustGit(t, dir, aliceEnv, "pull", "-q", "origin", "main")
	for _, f := range []string{"a.txt", "b.txt"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Fatalf("squashed content missing %s", f)
		}
	}

	// --- rebase: two commits replayed onto a diverged target ---
	mustGit(t, bobDir, bobEnv, "checkout", "-q", "-b", "feat2", "origin/main")
	mustGit(t, bobDir, bobEnv, "fetch", "-q", "origin")
	mustGit(t, bobDir, bobEnv, "reset", "-q", "--hard", "origin/main")
	os.WriteFile(filepath.Join(bobDir, "c.txt"), []byte("c\n"), 0o644)
	mustGit(t, bobDir, bobEnv, "add", ".")
	mustGit(t, bobDir, bobEnv, "commit", "-q", "-m", "step one")
	os.WriteFile(filepath.Join(bobDir, "d.txt"), []byte("d\n"), 0o644)
	mustGit(t, bobDir, bobEnv, "add", ".")
	mustGit(t, bobDir, bobEnv, "commit", "-q", "-m", "step two")
	mustGit(t, bobDir, bobEnv, "push", "-q", "origin", "feat2")
	if _, errOut, code := inst.ssh(t, bobKey, "", "mr", "create", "alice/lib",
		"--source", "feat2", "--target", "main", "--title", "'rebase me'"); code != 0 {
		t.Fatalf("mr2 create: %s", errOut)
	}
	mustGit(t, dir, aliceEnv, "commit", "-q", "--allow-empty", "-m", "mainline again")
	mustGit(t, dir, aliceEnv, "push", "-q", "origin", "main")

	before = strings.TrimSpace(mustGit(t, dir, aliceEnv, "rev-parse", "origin/main"))
	out, errOut, code = inst.ssh(t, aliceKey, "", "mr", "merge", "alice/lib", "2", "--strategy", "rebase", "--json")
	if code != 0 {
		t.Fatalf("rebase merge: %s", errOut)
	}
	mustGit(t, dir, aliceEnv, "fetch", "-q", "origin")
	// Two commits, linear (no merges), authors preserved, committer alice.
	count = strings.TrimSpace(mustGit(t, dir, aliceEnv, "rev-list", "--count", before+"..origin/main"))
	if count != "2" {
		t.Fatalf("rebase added %s commits, want 2", count)
	}
	merges := strings.TrimSpace(mustGit(t, dir, aliceEnv, "rev-list", "--merges", "--count", before+"..origin/main"))
	if merges != "0" {
		t.Fatal("rebase produced a merge commit")
	}
	logOut := mustGit(t, dir, aliceEnv, "log", "--format=%ae|%ce|%s", before+"..origin/main")
	for _, line := range strings.Split(strings.TrimSpace(logOut), "\n") {
		p := strings.Split(line, "|")
		if p[0] != "t@example.test" || p[1] != "alice@example.test" {
			t.Fatalf("rebase identities: %s", line)
		}
	}
	if !strings.Contains(logOut, "step one") || !strings.Contains(logOut, "step two") {
		t.Fatalf("rebase messages: %s", logOut)
	}

	// --- rebase refuses merge commits in the source ---
	mustGit(t, bobDir, bobEnv, "checkout", "-q", "-b", "feat3")
	mustGit(t, bobDir, bobEnv, "fetch", "-q", "origin")
	mustGit(t, bobDir, bobEnv, "reset", "-q", "--hard", "origin/main")
	mustGit(t, bobDir, bobEnv, "checkout", "-q", "-b", "side")
	os.WriteFile(filepath.Join(bobDir, "e.txt"), []byte("e\n"), 0o644)
	mustGit(t, bobDir, bobEnv, "add", ".")
	mustGit(t, bobDir, bobEnv, "commit", "-q", "-m", "side work")
	mustGit(t, bobDir, bobEnv, "checkout", "-q", "feat3")
	os.WriteFile(filepath.Join(bobDir, "f.txt"), []byte("f\n"), 0o644)
	mustGit(t, bobDir, bobEnv, "add", ".")
	mustGit(t, bobDir, bobEnv, "commit", "-q", "-m", "main work")
	mustGit(t, bobDir, bobEnv, "merge", "-q", "--no-ff", "-m", "internal merge", "side")
	mustGit(t, bobDir, bobEnv, "push", "-q", "origin", "feat3")
	if _, errOut, code := inst.ssh(t, bobKey, "", "mr", "create", "alice/lib",
		"--source", "feat3", "--target", "main", "--title", "'has a merge'"); code != 0 {
		t.Fatalf("mr3 create: %s", errOut)
	}
	mustGit(t, dir, aliceEnv, "fetch", "-q", "origin")
	mustGit(t, dir, aliceEnv, "reset", "-q", "--hard", "origin/main")
	mustGit(t, dir, aliceEnv, "commit", "-q", "--allow-empty", "-m", "diverge again")
	mustGit(t, dir, aliceEnv, "push", "-q", "origin", "main")
	_, errOut, code = inst.ssh(t, aliceKey, "", "mr", "merge", "alice/lib", "3", "--strategy", "rebase")
	if code != 2 || !strings.Contains(errOut, "linear history") {
		t.Fatalf("rebase with merge commit: exit %d, %s", code, errOut)
	}

	// --- require_signed_commits refuses squash outright ---
	if _, _, code := inst.ssh(t, aliceKey, "", "repo", "settings", "require-signed", "alice/lib", "on"); code != 0 {
		t.Fatal("require-signed failed")
	}
	_, errOut, code = inst.ssh(t, aliceKey, "", "mr", "merge", "alice/lib", "3", "--strategy", "squash")
	if code != 4 || !strings.Contains(errOut, "only fast-forward") {
		t.Fatalf("squash on require-signed: exit %d, %s", code, errOut)
	}
	if _, _, code := inst.ssh(t, aliceKey, "", "repo", "settings", "require-signed", "alice/lib", "off"); code != 0 {
		t.Fatal("require-signed off failed")
	}

	// --- rebase when ff is possible IS a fast-forward: shas preserved ---
	mustGit(t, bobDir, bobEnv, "checkout", "-q", "-b", "feat4")
	mustGit(t, bobDir, bobEnv, "fetch", "-q", "origin")
	mustGit(t, bobDir, bobEnv, "reset", "-q", "--hard", "origin/main")
	os.WriteFile(filepath.Join(bobDir, "g.txt"), []byte("g\n"), 0o644)
	mustGit(t, bobDir, bobEnv, "add", ".")
	mustGit(t, bobDir, bobEnv, "commit", "-q", "-m", "clean on top")
	tip := strings.TrimSpace(mustGit(t, bobDir, bobEnv, "rev-parse", "HEAD"))
	mustGit(t, bobDir, bobEnv, "push", "-q", "origin", "feat4")
	if _, errOut, code := inst.ssh(t, bobKey, "", "mr", "create", "alice/lib",
		"--source", "feat4", "--target", "main", "--title", "'ff-able'"); code != 0 {
		t.Fatalf("mr4 create: %s", errOut)
	}
	out, errOut, code = inst.ssh(t, aliceKey, "", "mr", "merge", "alice/lib", "4", "--strategy", "rebase", "--json")
	if code != 0 {
		t.Fatalf("ff-able rebase: %s", errOut)
	}
	if !strings.Contains(out, `"strategy":"ff"`) || !strings.Contains(out, tip) {
		t.Fatalf("ff-able rebase should fast-forward to %s: %s", tip, out)
	}
}
