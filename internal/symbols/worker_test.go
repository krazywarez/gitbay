package symbols

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gitbay.org/gitbay/internal/store"
)

type fixture struct {
	t    *testing.T
	st   *store.Store
	repo store.Repo
	src  string
	bare string
	w    *Worker
}

func newFixture(t *testing.T) *fixture {
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
	id, err := st.CreateRepo("user", uid, "app", "public")
	if err != nil {
		t.Fatal(err)
	}
	repo, err := st.RepoByID(id)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	f := &fixture{t: t, st: st, repo: repo, src: filepath.Join(root, "src"), bare: filepath.Join(root, "app.git")}
	f.w = &Worker{St: st, RepoDir: func(owner, name string) string { return f.bare },
		MaxSymbols: DefaultMaxSymbols, MaxTime: DefaultMaxTime}
	f.git(root, "init", "-q", "-b", repo.DefaultBranch, "src")
	f.git(root, "init", "-q", "--bare", f.bare)
	return f
}

func (f *fixture) git(dir string, args ...string) string {
	f.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.test",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.test")
	out, err := cmd.CombinedOutput()
	if err != nil {
		f.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// commit writes files (an empty content removes one) and pushes the
// default branch to the bare repository.
func (f *fixture) commit(files map[string]string) string {
	f.t.Helper()
	for name, content := range files {
		p := filepath.Join(f.src, name)
		if content == "" {
			os.Remove(p)
			continue
		}
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			f.t.Fatal(err)
		}
	}
	f.git(f.src, "add", "-A")
	f.git(f.src, "commit", "-q", "--allow-empty", "-m", "c")
	f.git(f.src, "push", "-q", "--force", f.bare, "HEAD:refs/heads/"+f.repo.DefaultBranch)
	return f.git(f.src, "rev-parse", "HEAD")
}

func (f *fixture) sweep(force bool) store.SymbolIndex {
	f.t.Helper()
	if err := f.st.RequestSymbolIndex(f.repo.ID, force); err != nil {
		f.t.Fatal(err)
	}
	f.w.Sweep(context.Background())
	if reqs, _ := f.st.SymbolRequests(); len(reqs) != 0 {
		f.t.Fatalf("request left after a sweep: %+v", reqs)
	}
	x, err := f.st.SymbolIndexFor(f.repo.ID)
	if err != nil {
		f.t.Fatal(err)
	}
	return x
}

func (f *fixture) names(x store.SymbolIndex) []string {
	f.t.Helper()
	rows, err := f.st.SearchSymbols(x.ID, "", "", 0, 0)
	if err != nil {
		f.t.Fatal(err)
	}
	var out []string
	for _, r := range rows {
		out = append(out, r.Path+":"+r.Name)
	}
	return out
}

func TestWorkerIndexesDefaultBranch(t *testing.T) {
	f := newFixture(t)
	head := f.commit(map[string]string{
		"main.go":           "package main\n\nfunc Hello() {}\n",
		"vendor/dep/dep.go": "package dep\n\nfunc Vendored() {}\n",
		"gen/zz_gen.go":     "package gen\n\nfunc Generated() {}\n",
		"big/big.go":        "package big\n\nfunc Big() {}\n" + strings.Repeat("//\n", MaxFileBytes/3),
		"docs/guide.md":     "# Guide\n",
		"assets/logo.png":   "\x89PNG\r\n",
		"node_modules/x.js": "function hidden() {}\n",
		"web/app.js":        "function shown() {}\n",
	})
	x := f.sweep(false)
	if x.State != "ok" || x.Commit != head {
		t.Fatalf("index = %+v, want ok at %s", x, head)
	}
	got := strings.Join(f.names(x), " ")
	for _, want := range []string{"main.go:Hello", "docs/guide.md:Guide", "web/app.js:shown"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %s in %s", want, got)
		}
	}
	for _, skip := range []string{"Vendored", "Generated", "Big", "hidden"} {
		if strings.Contains(got, skip) {
			t.Errorf("%s indexed: %s", skip, got)
		}
	}
	if x.Files != 3 || x.Symbols != 3 {
		t.Errorf("files %d symbols %d, want 3 and 3", x.Files, x.Symbols)
	}
}

// A new commit whose tree is the indexed one is not indexed again; a
// changed tree replaces the index, and force rebuilds an unchanged one.
func TestWorkerKeysOnTree(t *testing.T) {
	f := newFixture(t)
	f.commit(map[string]string{"a.go": "package a\n\nfunc One() {}\n"})
	first := f.sweep(false)

	f.commit(nil) // same tree, new commit
	same := f.sweep(false)
	if same.ID != first.ID || same.Commit != first.Commit {
		t.Fatalf("unchanged tree was reindexed: %+v then %+v", first, same)
	}

	f.commit(map[string]string{"a.go": "package a\n\nfunc Two() {}\n"})
	changed := f.sweep(false)
	if changed.ID == first.ID || changed.Tree == first.Tree {
		t.Fatalf("changed tree kept the old index: %+v", changed)
	}
	if got := f.names(changed); len(got) != 1 || got[0] != "a.go:Two" {
		t.Fatalf("symbols = %v, want only the new tree's", got)
	}
	var n int
	f.st.DB.QueryRow("SELECT COUNT(*) FROM symbol_indexes WHERE repo_id = ?", f.repo.ID).Scan(&n)
	if n != 1 {
		t.Fatalf("%d indexes for one repository, want 1", n)
	}

	forced := f.sweep(true)
	if forced.ID == changed.ID || forced.Tree != changed.Tree {
		t.Fatalf("force did not rebuild: %+v then %+v", changed, forced)
	}
}

func TestWorkerSymbolCap(t *testing.T) {
	f := newFixture(t)
	f.commit(map[string]string{"a.go": "package a\n\nfunc A() {}\nfunc B() {}\nfunc C() {}\n"})
	f.w.MaxSymbols = 2
	x := f.sweep(false)
	if x.State != "partial" || x.Symbols != 2 || !strings.Contains(x.Note, "2 symbols") {
		t.Fatalf("index = %+v, want partial with 2 symbols", x)
	}
}

func TestWorkerTimeBound(t *testing.T) {
	f := newFixture(t)
	f.commit(map[string]string{"a.go": "package a\n\nfunc A() {}\n"})
	f.w.MaxTime = time.Nanosecond
	x := f.sweep(false)
	if x.State != "partial" || !strings.Contains(x.Note, "stopped after") {
		t.Fatalf("index = %+v, want partial on the time bound", x)
	}
}

// A tree that cannot be read is recorded as failed, and the record stands
// for that tree: asking again does not retry it.
func TestWorkerFailureIsRecordedNotRetried(t *testing.T) {
	f := newFixture(t)
	f.commit(map[string]string{"a.go": "package a\n\nfunc A() {}\n"})
	blob := f.git(f.bare, "rev-parse", "refs/heads/"+f.repo.DefaultBranch+":a.go")
	if err := os.Remove(filepath.Join(f.bare, "objects", blob[:2], blob[2:])); err != nil {
		t.Fatal(err)
	}
	x := f.sweep(false)
	if x.State != "failed" || x.Note == "" || x.Symbols != 0 {
		t.Fatalf("index = %+v, want failed with a note", x)
	}
	again := f.sweep(false)
	if again.ID != x.ID {
		t.Fatalf("a failed tree was retried: %+v then %+v", x, again)
	}
}

func TestWorkerIgnoresEmptyRepository(t *testing.T) {
	f := newFixture(t)
	f.st.RequestSymbolIndex(f.repo.ID, false)
	f.w.Sweep(context.Background())
	if _, err := f.st.SymbolIndexFor(f.repo.ID); err != store.ErrNotFound {
		t.Fatalf("an empty repository has an index: %v", err)
	}
	if reqs, _ := f.st.SymbolRequests(); len(reqs) != 0 {
		t.Fatalf("request left: %+v", reqs)
	}
}
