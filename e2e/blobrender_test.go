package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Markdown and org files render on the blob page, with the source one
// click away; everything else is unchanged.
func TestBlobRendersMarkup(t *testing.T) {
	inst := startInstance(t)
	aliceKey := inst.newKey(t, "alice")
	inst.admin(t, "admin", "user", "create", "alice", "--key", aliceKey+".pub")
	if _, _, code := inst.ssh(t, aliceKey, "", "repo", "create", "alice/docs"); code != 0 {
		t.Fatal("repo create failed")
	}
	work := t.TempDir()
	env := inst.gitEnv(aliceKey)
	mustGit(t, work, env, "clone", inst.sshURL("alice/docs"), "w")
	dir := filepath.Join(work, "w")
	os.MkdirAll(filepath.Join(dir, "docs"), 0o755)
	os.WriteFile(filepath.Join(dir, "docs", "notes.md"), []byte("# Field notes\n\nSee [the plan](plan.org).\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "docs", "plan.org"), []byte("* The plan\n\nA *bold* claim.\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o644)
	mustGit(t, dir, env, "checkout", "-q", "-b", "main")
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, env, "commit", "-q", "-m", "docs")
	mustGit(t, dir, env, "push", "-q", "origin", "main")

	status, body := inst.get(t, "/alice/docs/blob/main/docs/notes.md")
	if status != 200 || !strings.Contains(body, `<h2 id="field-notes">Field notes</h2>`) {
		t.Fatalf("markdown not rendered: %d\n%s", status, body)
	}
	if !strings.Contains(body, `href="?view=source"`) || !strings.Contains(body, "<strong>rendered</strong>") {
		t.Fatalf("no source toggle:\n%s", body)
	}
	// A relative link resolves against the file's directory, as in a README.
	if !strings.Contains(body, "/alice/docs/blob/main/docs/plan.org") {
		t.Fatalf("relative link not rewritten:\n%s", body)
	}
	_, body = inst.get(t, "/alice/docs/blob/main/docs/notes.md?view=source")
	if !strings.Contains(body, `class="code"`) || strings.Contains(body, "Field notes</h1>") || !strings.Contains(body, `href="?"`) {
		t.Fatalf("source view wrong:\n%s", body)
	}
	_, body = inst.get(t, "/alice/docs/blob/main/docs/plan.org")
	if !strings.Contains(body, "The plan") || !strings.Contains(body, "<strong>bold</strong>") || !strings.Contains(body, `href="?view=source"`) {
		t.Fatalf("org not rendered:\n%s", body)
	}
	_, body = inst.get(t, "/alice/docs/blob/main/main.go")
	if strings.Contains(body, "view=source") || !strings.Contains(body, `class="code"`) {
		t.Fatalf("plain file gained a toggle or lost its code view:\n%s", body)
	}
}
