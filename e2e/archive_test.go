package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestArchiveAndTopics(t *testing.T) {
	inst := startInstance(t)
	aliceKey := inst.newKey(t, "alice")
	bobKey := inst.newKey(t, "bob")
	inst.admin(t, "admin", "user", "create", "alice",
		"--key", aliceKey+".pub", "--email", "alice@example.test", "--verified")
	inst.admin(t, "admin", "user", "create", "bob", "--key", bobKey+".pub")

	if _, errOut, code := inst.ssh(t, aliceKey, "", "repo", "create", "alice/app"); code != 0 {
		t.Fatalf("repo create: %s", errOut)
	}
	if _, _, code := inst.ssh(t, aliceKey, "", "repo", "access", "grant", "alice/app", "bob", "write"); code != 0 {
		t.Fatal("grant failed")
	}
	work := t.TempDir()
	env := inst.gitEnv(aliceKey)
	mustGit(t, work, env, "clone", inst.sshURL("alice/app"), "w")
	dir := filepath.Join(work, "w")
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a\n"), 0o644)
	mustGit(t, dir, env, "checkout", "-q", "-b", "main")
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, env, "commit", "-q", "-m", "base")
	mustGit(t, dir, env, "push", "-q", "origin", "main")

	// Topics: admin-only edits, validation, idempotent add, listing.
	if _, errOut, code := inst.ssh(t, bobKey, "", "repo", "topics", "add", "alice/app", "go"); code != 4 {
		t.Fatalf("non-admin added topics: exit %d, %s", code, errOut)
	}
	if _, errOut, code := inst.ssh(t, aliceKey, "", "repo", "topics", "add", "alice/app", "Bad_Topic"); code != 2 || !strings.Contains(errOut, "invalid topic") {
		t.Fatalf("bad topic accepted: exit %d, %s", code, errOut)
	}
	if _, errOut, code := inst.ssh(t, aliceKey, "", "repo", "topics", "add", "alice/app", "go", "cli", "go"); code != 0 {
		t.Fatalf("topics add: %s", errOut)
	}
	out, _, _ := inst.ssh(t, aliceKey, "", "repo", "topics", "alice/app", "--json")
	if !strings.Contains(out, `["cli","go"]`) {
		t.Fatalf("topics list: %s", out)
	}
	out, _, _ = inst.ssh(t, aliceKey, "", "repo", "show", "alice/app", "--json")
	if !strings.Contains(out, `"topics":["cli","go"]`) || strings.Contains(out, `"archived"`) {
		t.Fatalf("repo show topics: %s", out)
	}
	if _, _, code := inst.ssh(t, aliceKey, "", "repo", "topics", "remove", "alice/app", "cli"); code != 0 {
		t.Fatal("topics remove failed")
	}
	if _, _, code := inst.ssh(t, aliceKey, "", "repo", "topics", "remove", "alice/app", "cli"); code != 3 {
		t.Fatal("removing an absent topic should be not-found")
	}

	// An MR and an issue that predate archiving.
	mustGit(t, dir, env, "checkout", "-q", "-b", "feat")
	os.WriteFile(filepath.Join(dir, "b.txt"), []byte("b\n"), 0o644)
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, env, "commit", "-q", "-m", "feat")
	mustGit(t, dir, env, "push", "-q", "origin", "feat")
	if _, errOut, code := inst.ssh(t, aliceKey, "", "mr", "create", "alice/app",
		"--source", "feat", "--target", "main", "--title", "'feat'"); code != 0 {
		t.Fatalf("mr create: %s", errOut)
	}
	if _, errOut, code := inst.ssh(t, aliceKey, "", "issue", "create", "alice/app", "--title", "'todo'"); code != 0 {
		t.Fatalf("issue create: %s", errOut)
	}

	// Archive: admin-only, then everything content-mutating is refused.
	if _, _, code := inst.ssh(t, bobKey, "", "repo", "archive", "alice/app"); code != 4 {
		t.Fatal("non-admin archived the repo")
	}
	if _, errOut, code := inst.ssh(t, aliceKey, "", "repo", "archive", "alice/app"); code != 0 {
		t.Fatalf("archive: %s", errOut)
	}
	if _, _, code := inst.ssh(t, aliceKey, "", "repo", "archive", "alice/app"); code != 2 {
		t.Fatal("double archive not refused")
	}

	os.WriteFile(filepath.Join(dir, "c.txt"), []byte("c\n"), 0o644)
	mustGit(t, dir, env, "checkout", "-q", "main")
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, env, "commit", "-q", "-m", "more")
	if out, code := gitRun(t, dir, env, "push", "origin", "main"); code == 0 || !strings.Contains(out, "archived and read-only") {
		t.Fatalf("push to archived repo: exit %d\n%s", code, out)
	}
	for _, cmd := range [][]string{
		{"issue", "create", "alice/app", "--title", "'x'"},
		{"issue", "comment", "alice/app", "1", "--message", "'x'"},
		{"issue", "close", "alice/app", "1"},
		{"mr", "comment", "alice/app", "1", "--message", "'x'"},
		{"mr", "review", "alice/app", "1", "--approve"},
		{"mr", "merge", "alice/app", "1"},
		{"mr", "close", "alice/app", "1"},
		{"status", "set", "alice/app", "HEAD", "--context", "ci", "--state", "success"},
	} {
		if _, errOut, code := inst.ssh(t, bobKey, "", cmd...); code != 4 || !strings.Contains(errOut, "archived and read-only") {
			t.Fatalf("%v on archived repo: exit %d, %s", cmd, code, errOut)
		}
	}

	// Reading stays untouched: clone, listings, show, web.
	work2 := t.TempDir()
	mustGit(t, work2, env, "clone", "-q", inst.sshURL("alice/app"), "r")
	if out, _, code := inst.ssh(t, aliceKey, "", "issue", "list", "alice/app"); code != 0 || !strings.Contains(out, "todo") {
		t.Fatalf("issue list on archived repo: %s", out)
	}
	out, _, _ = inst.ssh(t, aliceKey, "", "repo", "show", "alice/app", "--json")
	if !strings.Contains(out, `"archived":true`) {
		t.Fatalf("repo show archived flag: %s", out)
	}
	if status, _ := inst.get(t, "/alice/app"); status != 200 {
		t.Fatalf("web browse on archived repo: %d", status)
	}

	// Settings and unarchive still work; writes come back.
	if _, _, code := inst.ssh(t, aliceKey, "", "repo", "settings", "protect", "alice/app", "main"); code != 0 {
		t.Fatal("settings on archived repo should still work")
	}
	if _, _, code := inst.ssh(t, aliceKey, "", "repo", "settings", "unprotect", "alice/app", "main"); code != 0 {
		t.Fatal("unprotect failed")
	}
	if _, errOut, code := inst.ssh(t, aliceKey, "", "repo", "unarchive", "alice/app"); code != 0 {
		t.Fatalf("unarchive: %s", errOut)
	}
	mustGit(t, dir, env, "push", "-q", "origin", "main")
	if _, errOut, code := inst.ssh(t, bobKey, "", "issue", "comment", "alice/app", "1", "--message", "'back'"); code != 0 {
		t.Fatalf("comment after unarchive: %s", errOut)
	}
}
