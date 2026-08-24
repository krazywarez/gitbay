package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadmeRelativeLinks(t *testing.T) {
	inst := startInstance(t)
	aliceKey := inst.newKey(t, "alice")
	inst.admin(t, "admin", "user", "create", "alice", "--key", aliceKey+".pub")
	if _, _, code := inst.ssh(t, aliceKey, "", "repo", "create", "alice/site"); code != 0 {
		t.Fatal("repo create failed")
	}
	work := t.TempDir()
	env := inst.gitEnv(aliceKey)
	mustGit(t, work, env, "clone", inst.sshURL("alice/site"), "w")
	dir := filepath.Join(work, "w")
	os.MkdirAll(filepath.Join(dir, "docs"), 0o755)
	os.MkdirAll(filepath.Join(dir, "img"), 0o755)
	os.WriteFile(filepath.Join(dir, "README.md"), []byte(
		"# site\n\n[guide](docs/guide.md) and [export](docs/paper.html) and "+
			"[abs](https://example.org/x) here\n\n![logo](img/logo.png)\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "docs", "guide.md"), []byte("# guide\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "docs", "paper.org"), []byte("* paper\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "img", "logo.png"), []byte{0x89, 0x50}, 0o644)
	mustGit(t, dir, env, "checkout", "-q", "-b", "main")
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, env, "commit", "-q", "-m", "base")
	mustGit(t, dir, env, "push", "-q", "origin", "main")

	status, body := inst.get(t, "/alice/site")
	if status != 200 {
		t.Fatalf("tree: %d", status)
	}
	for _, want := range []string{
		`href="/alice/site/blob/main/docs/guide.md"`,   // relative link
		`href="/alice/site/blob/main/docs/paper.org"`,  // .html mapped to .org source
		`src="/alice/site/raw/main/img/logo.png"`,      // relative image via raw
		`href="https://example.org/x"`,                 // absolute untouched
		`href="/alice/site/blob/main/README.md">README.md</a>`, // clickable card header
		`<th>name</th>`, // file table column headers
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q", want)
		}
	}
	// Branch dropdown lists branches.
	if !strings.Contains(body, `class="refmenu"`) || !strings.Contains(body, ">all refs") {
		t.Error("branch dropdown missing")
	}
	// Explore rows carry topics, license, and updated date.
	inst.ssh(t, aliceKey, "", "repo", "topics", "add", "alice/site", "web")
	os.WriteFile(filepath.Join(dir, "LICENSE"), []byte("Permission to use, copy, modify, and/or distribute this software...\n"), 0o644)
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, env, "commit", "-q", "-m", "license")
	mustGit(t, dir, env, "push", "-q", "origin", "main")
	_, body = inst.get(t, "/explore")
	for _, want := range []string{`href="/explore?q=web"`, "ISC", "updated 20"} {
		if !strings.Contains(body, want) {
			t.Errorf("explore row missing %q", want)
		}
	}
}
