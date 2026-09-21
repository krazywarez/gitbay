package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A file page lists its directory beside the file, marks the file, and
// links up (desktop layout spec).
func TestFileNavigator(t *testing.T) {
	t.Parallel()
	inst := startInstanceWith(t, "[web]\nmode = \"accounts\"\n")
	key := inst.newKey(t, "alice")
	inst.admin(t, "admin", "user", "create", "alice", "--key", key+".pub")
	if _, errOut, code := inst.ssh(t, key, "", "repo", "create", "alice/nav"); code != 0 {
		t.Fatalf("repo create: %s", errOut)
	}
	// the same clone-commit-push shape TestWebUI uses (e2e/web_test.go:41-50)
	work := t.TempDir()
	env := inst.gitEnv(key)
	mustGit(t, work, env, "clone", inst.sshURL("alice/nav"), "w")
	dir := filepath.Join(work, "w")
	os.MkdirAll(filepath.Join(dir, "cmd", "sub"), 0o755)
	os.WriteFile(filepath.Join(dir, "README.md"), []byte("# nav\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "cmd", "main.go"), []byte("package main\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "cmd", "sub", "x.go"), []byte("package sub\n"), 0o644)
	mustGit(t, dir, env, "checkout", "-q", "-b", "main")
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, env, "commit", "-q", "-m", "one")
	mustGit(t, dir, env, "push", "-q", "origin", "main")

	status, body := inst.get(t, "/alice/nav/blob/main/cmd/main.go")
	if status != 200 {
		t.Fatalf("blob: %d", status)
	}
	for _, want := range []string{
		`<nav class="filenav" aria-label="Files">`,
		`<h2 class="colhead">cmd</h2>`,
		`<a class="up" href="/alice/nav/tree/main">..</a>`,
		`<a class="dir" href="/alice/nav/tree/main/cmd/sub">sub/</a>`,
		`<a aria-current="page" href="/alice/nav/blob/main/cmd/main.go">main.go</a>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("blob page lacks %q", want)
		}
	}
	_, body = inst.get(t, "/alice/nav/blame/main/README.md")
	if !strings.Contains(body, `<h2 class="colhead">nav</h2>`) || !strings.Contains(body, `<a aria-current="page" href="/alice/nav/blob/main/README.md">README.md</a>`) {
		t.Errorf("blame page lacks the root navigator:\n%s", body)
	}

	alice := inst.login(t, key)
	status, body = browserGet(t, alice, inst.base()+"/alice/nav/edit/main/cmd/main.go")
	if status != 200 || !strings.Contains(body, `<h2 class="colhead">cmd</h2>`) || !strings.Contains(body, `<a aria-current="page" href="/alice/nav/blob/main/cmd/main.go">main.go</a>`) {
		t.Errorf("edit page lacks the navigator: %d\n%s", status, body)
	}
}
