package httpd

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gitbay.org/gitbay/internal/config"
	"gitbay.org/gitbay/internal/control"
	"gitbay.org/gitbay/internal/store"
	"gitbay.org/gitbay/internal/symbols"
)

func symbolGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.test",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.test")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// symbolServer serves alice/app (public) and alice/secret (private), each
// with main indexed and a side branch whose tree differs.
func symbolServer(t *testing.T) *Server {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.MigrateUp(); err != nil {
		t.Fatal(err)
	}
	uid, err := st.CreateUser("alice", false)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	src := filepath.Join(root, "src")
	symbolGit(t, root, "init", "-q", "-b", "main", "src")
	files := map[string]string{
		"main.go":  "package main\n\nfunc main() { helper(); dup(); unknown() }\n\nfunc helper() {}\n",
		"a/dup.go": "package a\n\nfunc dup() {}\n",
		"b/dup.go": "package b\n\nfunc dup() {}\n",
	}
	for name, body := range files {
		os.MkdirAll(filepath.Join(src, filepath.Dir(name)), 0o755)
		os.WriteFile(filepath.Join(src, name), []byte(body), 0o644)
	}
	symbolGit(t, src, "add", ".")
	symbolGit(t, src, "commit", "-q", "-m", "base")
	symbolGit(t, src, "checkout", "-q", "-b", "side")
	os.WriteFile(filepath.Join(src, "side.go"), []byte("package main\n"), 0o644)
	symbolGit(t, src, "add", ".")
	symbolGit(t, src, "commit", "-q", "-m", "side")

	w := &symbols.Worker{St: st, RepoDir: func(owner, name string) string { return control.RepoDir(root, owner, name) },
		MaxSymbols: symbols.DefaultMaxSymbols, MaxTime: symbols.DefaultMaxTime}
	for _, r := range []struct{ name, vis string }{{"app", "public"}, {"secret", "private"}} {
		id, err := st.CreateRepo("user", uid, r.name, r.vis)
		if err != nil {
			t.Fatal(err)
		}
		dir := control.RepoDir(root, "alice", r.name)
		os.MkdirAll(filepath.Dir(dir), 0o755)
		symbolGit(t, root, "init", "-q", "--bare", dir)
		symbolGit(t, src, "push", "-q", dir, "main", "side")
		if err := w.Index(context.Background(), id, false); err != nil {
			t.Fatal(err)
		}
	}
	cfg := config.Default()
	cfg.Server.Root = root
	cfg.Server.SiteURL = "https://forge.test/"
	return New(cfg, st, nil)
}

func TestBlobLinksIndexedNames(t *testing.T) {
	h := symbolServer(t).Handler()
	w := get(t, h, "/alice/app/blob/main/main.go", nil)
	if w.Code != 200 {
		t.Fatalf("blob: %d", w.Code)
	}
	body := w.Body.String()
	for _, want := range []string{
		// defined once: straight to the line, at the ref being viewed
		`<a class="sym" href="/alice/app/blob/main/main.go#L5"><span class="nf">helper</span></a>`,
		// defined twice: to the results page
		`<a class="sym" href="/alice/app/symbols?q=dup"><span class="nf">dup</span></a>`,
		// the per-file list
		`2 symbols in this file`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("blob page lacks %s", want)
		}
	}
	if strings.Contains(body, `>unknown</span></a>`) {
		t.Error("a name the index does not hold is linked")
	}

	// A branch at another tree is not what the index describes.
	w = get(t, h, "/alice/app/blob/side/main.go", nil)
	if w.Code != 200 || strings.Contains(w.Body.String(), `class="sym"`) || strings.Contains(w.Body.String(), "in this file") {
		t.Errorf("side branch: %d, links or list present", w.Code)
	}
}

func TestSymbolsPage(t *testing.T) {
	h := symbolServer(t).Handler()
	w := get(t, h, "/alice/app/symbols?q=dup", nil)
	body := w.Body.String()
	if w.Code != 200 || !strings.Contains(body, "a/dup.go:3") || !strings.Contains(body, "b/dup.go:3") {
		t.Fatalf("results page: %d\n%s", w.Code, body)
	}
	w = get(t, h, "/alice/app/symbols?q=dup&kind=type", nil)
	if strings.Contains(w.Body.String(), "a/dup.go:3") {
		t.Error("kind filter ignored")
	}
	for _, p := range []string{"/alice/secret/symbols?q=dup", "/alice/secret/blob/main/main.go"} {
		if w := get(t, h, p, nil); w.Code != 404 {
			t.Errorf("%s anonymously: %d, want 404", p, w.Code)
		}
	}
}
