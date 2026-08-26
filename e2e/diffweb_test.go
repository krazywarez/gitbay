package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDiffRendering covers the shared diff view: per-file folds with stats,
// line-number gutters, syntax highlighting, and binary files declared
// rather than dumped.
func TestDiffRendering(t *testing.T) {
	inst := startInstance(t)
	aliceKey := inst.newKey(t, "alice")
	inst.admin(t, "admin", "user", "create", "alice", "--key", aliceKey+".pub")
	if _, errOut, code := inst.ssh(t, aliceKey, "", "repo", "create", "alice/app"); code != 0 {
		t.Fatalf("repo create: %s", errOut)
	}

	work := t.TempDir()
	env := inst.gitEnv(aliceKey)
	mustGit(t, work, env, "clone", inst.sshURL("alice/app"), "w")
	dir := filepath.Join(work, "w")
	write := func(name, body string) {
		os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644)
	}
	write("main.go", "package main\n\nfunc greet() string {\n\treturn \"hello\"\n}\n")
	write("notes.txt", "old title\n")
	mustGit(t, dir, env, "checkout", "-q", "-b", "main")
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, env, "commit", "-q", "-m", "base")
	mustGit(t, dir, env, "push", "-q", "origin", "main")

	// One commit touching three files in three different ways.
	write("main.go", "package main\n\nfunc greet() string {\n\t// now with feeling\n\treturn \"HELLO\"\n}\n")
	os.Rename(filepath.Join(dir, "notes.txt"), filepath.Join(dir, "README.md"))
	os.WriteFile(filepath.Join(dir, "logo.png"), []byte("\x89PNG\r\n\x1a\n\x00\x00binary"), 0o644)
	mustGit(t, dir, env, "add", "-A")
	mustGit(t, dir, env, "commit", "-q", "-m", "rework")
	mustGit(t, dir, env, "push", "-q", "origin", "main")

	out, _, _ := inst.ssh(t, aliceKey, "", "repo", "log", "alice/app", "--json")
	sha := jsonField(out, "sha")
	if sha == "" {
		t.Fatalf("no sha in log: %s", out)
	}

	status, body := inst.get(t, "/alice/app/commit/"+sha)
	if status != 200 {
		t.Fatalf("commit page: %d", status)
	}

	// A fold per file, each naming its path.
	for _, path := range []string{"main.go", "logo.png"} {
		if !strings.Contains(body, ">"+path+"<") {
			t.Errorf("no section for %s", path)
		}
	}
	if strings.Count(body, `<details class="difffold"`) != 3 {
		t.Errorf("want 3 file sections, got %d", strings.Count(body, `<details class="difffold"`))
	}
	// The rename is shown as one, not as an add plus a delete.
	if !strings.Contains(body, "notes.txt") || !strings.Contains(body, "renamed") {
		t.Error("rename not shown as a rename")
	}
	// Binary content is declared, never dumped into the page.
	if !strings.Contains(body, "Binary file not shown") {
		t.Error("binary file not declared")
	}
	if strings.Contains(body, "\x89PNG") {
		t.Error("binary content leaked into the diff")
	}
	// Line-number gutters and per-file stats.
	if !strings.Contains(body, `<td class="ln">`) {
		t.Error("no line-number gutter")
	}
	if !strings.Contains(body, `<span class="add">+`) || !strings.Contains(body, `<span class="del">−`) {
		t.Error("no per-file stat")
	}
	// Go is a type chroma knows, so the added line carries token markup.
	if !strings.Contains(body, "class=\"k\"") && !strings.Contains(body, "class=\"kd\"") {
		t.Error("diff content is not syntax highlighted")
	}
	// The +/- markers are CSS, so a copied selection is real source.
	if strings.Contains(body, `<td class="src">+`) {
		t.Error("diff markers are in the markup, not the stylesheet")
	}
	// The cell must not use .code: that class carries its own background,
	// which would paint over the add/delete row tint.
	if strings.Contains(body, `<td class="code">`) {
		t.Error("diff cells use the blob code class, whose background hides the row tint")
	}
}

// jsonField pulls the first "name":"value" string out of a JSON blob.
func jsonField(blob, name string) string {
	key := `"` + name + `":"`
	i := strings.Index(blob, key)
	if i < 0 {
		return ""
	}
	rest := blob[i+len(key):]
	j := strings.IndexByte(rest, '"')
	if j < 0 {
		return ""
	}
	return rest[:j]
}
