package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCommitMessageIssueActions(t *testing.T) {
	inst := startInstance(t)
	aliceKey := inst.newKey(t, "alice")
	inst.admin(t, "admin", "user", "create", "alice",
		"--key", aliceKey+".pub", "--email", "alice@example.test", "--verified")

	if _, errOut, code := inst.ssh(t, aliceKey, "", "repo", "create", "alice/app"); code != 0 {
		t.Fatalf("repo create: %s", errOut)
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
	for _, title := range []string{"'one'", "'two'", "'three'"} {
		if _, _, code := inst.ssh(t, aliceKey, "", "issue", "create", "alice/app", "--title", title); code != 0 {
			t.Fatal("issue create failed")
		}
	}

	// A closing keyword on the default branch closes the issue with a
	// comment; a bare reference (and a nonexistent #99) only comments.
	os.WriteFile(filepath.Join(dir, "b.txt"), []byte("b\n"), 0o644)
	aliceEnv := append(append([]string{}, env...),
		"GIT_AUTHOR_NAME=Alice", "GIT_AUTHOR_EMAIL=alice@example.test")
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, aliceEnv, "commit", "-q", "-m", "repair the widget\n\nFixes #1. Related to #2 but not #99.")
	mustGit(t, dir, env, "push", "-q", "origin", "main")

	out, _, _ := inst.ssh(t, aliceKey, "", "issue", "show", "alice/app", "1", "--json")
	if !strings.Contains(out, `"state":"closed"`) || !strings.Contains(out, "closed by commit") {
		t.Fatalf("issue 1 not closed by commit: %s", out)
	}
	// The entry is a system message with a linked sha, not a user comment.
	if !strings.Contains(out, `"author":"system"`) || !strings.Contains(out, "](/alice/app/commit/") {
		t.Fatalf("close entry not a linked system message: %s", out)
	}
	if status, body := inst.get(t, "/alice/app/issues/1"); status != 200 ||
		!strings.Contains(body, `class="syscomment"`) || !strings.Contains(body, `/alice/app/commit/`) {
		t.Fatalf("web system message: %d", status)
	}
	out, _, _ = inst.ssh(t, aliceKey, "", "issue", "show", "alice/app", "2", "--json")
	if !strings.Contains(out, `"state":"open"`) || !strings.Contains(out, "referenced in commit") ||
		!strings.Contains(out, "repair the widget") {
		t.Fatalf("issue 2 not referenced: %s", out)
	}
	// The reference names who wrote the commit, and links them when the
	// author email is verified on an account here.
	if !strings.Contains(out, "by [alice](/alice)") {
		t.Fatalf("reference does not attribute the author: %s", out)
	}
	if status, body := inst.get(t, "/alice/app/issues/2"); status != 200 ||
		!strings.Contains(body, `href="/alice"`) {
		t.Fatalf("web reference does not link the author: %d\n%s", status, body)
	}

	// Commits on a feature branch do nothing until they land on the
	// default branch via a merge — then the merge path acts exactly once.
	mustGit(t, dir, env, "checkout", "-q", "-b", "feat")
	os.WriteFile(filepath.Join(dir, "c.txt"), []byte("c\n"), 0o644)
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, env, "commit", "-q", "-m", "finish the gadget\n\nCloses #3")
	mustGit(t, dir, env, "push", "-q", "origin", "feat")
	out, _, _ = inst.ssh(t, aliceKey, "", "issue", "show", "alice/app", "3", "--json")
	if !strings.Contains(out, `"state":"open"`) {
		t.Fatalf("branch push acted early: %s", out)
	}
	if _, _, code := inst.ssh(t, aliceKey, "", "mr", "create", "alice/app",
		"--source", "feat", "--target", "main", "--title", "'gadget'"); code != 0 {
		t.Fatal("mr create failed")
	}
	if _, errOut, code := inst.ssh(t, aliceKey, "", "mr", "merge", "alice/app", "1"); code != 0 {
		t.Fatalf("merge: %s", errOut)
	}
	out, _, _ = inst.ssh(t, aliceKey, "", "issue", "show", "alice/app", "3", "--json")
	if !strings.Contains(out, `"state":"closed"`) || !strings.Contains(out, "closed by commit") {
		t.Fatalf("merge did not close issue 3: %s", out)
	}
	// This one was authored by an address nobody has verified, so it names
	// git's author without inventing a profile link for them.
	if !strings.Contains(out, "by t:") || strings.Contains(out, "by [t]") {
		t.Fatalf("unresolved author should stay plain text: %s", out)
	}
	if strings.Count(out, "closed by commit") != 1 {
		t.Fatalf("duplicate close comments: %s", out)
	}

	// Pushing more commits does not re-act on already-processed shas.
	os.WriteFile(filepath.Join(dir, "d.txt"), []byte("d\n"), 0o644)
	mustGit(t, dir, env, "checkout", "-q", "main")
	mustGit(t, dir, env, "pull", "-q", "origin", "main")
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, env, "commit", "-q", "-m", "unrelated")
	mustGit(t, dir, env, "push", "-q", "origin", "main")
	out, _, _ = inst.ssh(t, aliceKey, "", "issue", "show", "alice/app", "2", "--json")
	if strings.Count(out, "referenced in commit") != 1 {
		t.Fatalf("reference duplicated: %s", out)
	}
}
