package symbols

import (
	"context"
	"fmt"
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
	f.w = NewWith(st, func(owner, name string) string { return f.bare }, 0)
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

// breakBlob deletes the loose object of path at the default branch, so
// the tree cannot be read.
func (f *fixture) breakBlob(path string) {
	f.t.Helper()
	blob := f.git(f.bare, "rev-parse", "refs/heads/"+f.repo.DefaultBranch+":"+path)
	if err := os.Remove(filepath.Join(f.bare, "objects", blob[:2], blob[2:])); err != nil {
		f.t.Fatal(err)
	}
}

// A tree that cannot be read is recorded as a failure beside the current
// index, which stays current. The request is kept for one retry after the
// backoff rather than tried again at once; a new request for the same
// tree inside the backoff is not retried either.
func TestWorkerFailureKeepsCurrentIndex(t *testing.T) {
	f := newFixture(t)
	f.commit(map[string]string{"a.go": "package a\n\nfunc A() {}\n"})
	good := f.sweep(false)

	f.commit(map[string]string{"b.go": "package a\n\nfunc B() {}\n"})
	f.breakBlob("b.go")
	f.st.RequestSymbolIndex(f.repo.ID, false)
	f.w.Sweep(context.Background())
	cur, err := f.st.SymbolIndexFor(f.repo.ID)
	if err != nil || cur.ID != good.ID {
		t.Fatalf("current index after a failure = %+v, %v; want %d", cur, err, good.ID)
	}
	fail, err := f.st.SymbolFailureFor(f.repo.ID)
	if err != nil || !strings.Contains(fail.Note, "b.go") {
		t.Fatalf("failure = %+v, %v", fail, err)
	}
	if reqs, _ := f.st.SymbolRequests(); len(reqs) != 0 {
		t.Fatalf("a failed request is due again at once: %+v", reqs)
	}
	var attempts int
	f.st.DB.QueryRow("SELECT attempts FROM symbol_requests WHERE repo_id = ?", f.repo.ID).Scan(&attempts)
	if attempts != 1 {
		t.Fatalf("attempts = %d, want the request kept for one retry", attempts)
	}

	// Asked again inside the backoff: the failed tree is left alone.
	f.st.RequestSymbolIndex(f.repo.ID, false)
	failed, err := f.w.Index(context.Background(), f.repo.ID, false)
	if failed || err != nil {
		t.Fatalf("a recently failed tree was retried: %v, %v", failed, err)
	}

	// After the backoff it is tried again, and still fails.
	f.w.Backoff = 0
	if failed, _ := f.w.Index(context.Background(), f.repo.ID, false); !failed {
		t.Fatal("a failed tree was not retried after the backoff")
	}

	// A second failure of a retry is the end of it.
	f.st.DB.Exec("DELETE FROM symbol_requests")
	f.st.DB.Exec("INSERT INTO symbol_requests (repo_id, attempts) VALUES (?, 1)", f.repo.ID)
	f.w.Sweep(context.Background())
	var n int
	f.st.DB.QueryRow("SELECT COUNT(*) FROM symbol_requests").Scan(&n)
	if n != 0 {
		t.Fatal("a failed retry was kept for another")
	}

	// A push that changes the tree builds, and clears the failure.
	f.git(f.src, "rm", "-q", "b.go")
	f.commit(map[string]string{"c.go": "package a\n\nfunc C() {}\n"})
	next := f.sweep(false)
	if next.ID == good.ID {
		t.Fatal("a new tree after a failure was not indexed")
	}
	if _, err := f.st.SymbolFailureFor(f.repo.ID); err != store.ErrNotFound {
		t.Fatalf("failure kept after a good index: %v", err)
	}
}

// A new index is written in chunks no read sees: throughout the build the
// old index is current, and the flip replaces it whole.
func TestWorkerBuildIsInvisibleUntilPublished(t *testing.T) {
	f := newFixture(t)
	f.commit(map[string]string{"a.go": "package a\n\nfunc Old() {}\n"})
	old := f.sweep(false)

	f.commit(map[string]string{"a.go": "package a\n\nfunc New1() {}\nfunc New2() {}\nfunc New3() {}\n"})
	f.w.ChunkRows = 1
	chunks := 0
	f.w.chunkHook = func(building int64) {
		chunks++
		cur, err := f.st.SymbolIndexFor(f.repo.ID)
		if err != nil || cur.ID != old.ID {
			t.Errorf("mid-build current index = %+v, %v; want the old one", cur, err)
		}
		if got := f.names(cur); len(got) != 1 || got[0] != "a.go:Old" {
			t.Errorf("mid-build reads %v", got)
		}
		if b, _ := f.st.SymbolIndexByID(building); b.State != "building" {
			t.Errorf("index being built is %q", b.State)
		}
	}
	next := f.sweep(false)
	if chunks != 3 {
		t.Fatalf("%d chunks, want one per row", chunks)
	}
	if got := f.names(next); len(got) != 3 {
		t.Fatalf("after the flip: %v", got)
	}
	var indexes, rows int
	f.st.DB.QueryRow("SELECT COUNT(*) FROM symbol_indexes").Scan(&indexes)
	f.st.DB.QueryRow("SELECT COUNT(*) FROM symbols").Scan(&rows)
	if indexes != 1 || rows != 3 {
		t.Fatalf("%d indexes and %d rows left, want the new index alone", indexes, rows)
	}
}

// A run that stopped mid-build leaves the old index current, and the next
// run deletes what it wrote.
func TestWorkerCleansUpInterruptedBuild(t *testing.T) {
	f := newFixture(t)
	f.commit(map[string]string{"a.go": "package a\n\nfunc Old() {}\n"})
	old := f.sweep(false)

	// What a crash between chunks leaves.
	orphan, err := f.st.BeginSymbolIndex(f.repo.ID, "c", "t")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.st.AddSymbols(orphan, []store.SymbolRow{{Name: "Half", Key: "Half", Kind: "function", Path: "x.go", Line: 1}}); err != nil {
		t.Fatal(err)
	}
	if cur, _ := f.st.SymbolIndexFor(f.repo.ID); cur.ID != old.ID {
		t.Fatalf("an unfinished build became current: %+v", cur)
	}

	f.commit(nil) // same tree: nothing to build, but the leftovers go
	f.sweep(false)
	if _, err := f.st.SymbolIndexByID(orphan); err != store.ErrNotFound {
		t.Fatalf("interrupted build still there: %v", err)
	}
	var rows int
	f.st.DB.QueryRow("SELECT COUNT(*) FROM symbols WHERE index_id = ?", orphan).Scan(&rows)
	if rows != 0 {
		t.Fatalf("%d rows of the interrupted build left", rows)
	}
}

// A hostile tree: names past the length cap are dropped, and long
// headings stop at the byte budget with a partial index saying so.
func TestWorkerHostileNames(t *testing.T) {
	f := newFixture(t)
	files := map[string]string{}
	huge := strings.Repeat("x", MaxNameBytes+1)
	for i := 0; i < 20; i++ {
		files[fmt.Sprintf("huge%02d.go", i)] = "package a\n\nfunc " + huge + "() {}\nvar " + huge + " int\n"
	}
	var md strings.Builder
	for i := 0; i < 500; i++ {
		fmt.Fprintf(&md, "# %03d %s\n", i, strings.Repeat("h", 200))
	}
	files["notes.md"] = md.String()
	f.commit(files)
	f.w.MaxBytes = 20_000
	x := f.sweep(false)
	if x.State != "partial" || !strings.Contains(x.Note, "20000 bytes") {
		t.Fatalf("index = %+v, want partial at the byte budget", x)
	}
	var longest, total int
	f.st.DB.QueryRow("SELECT COALESCE(MAX(length(name)), 0), COALESCE(SUM(length(name) + length(key) + length(path)), 0) FROM symbols").
		Scan(&longest, &total)
	if longest > MaxNameBytes {
		t.Errorf("a %d-byte name was kept", longest)
	}
	if total > 20_000 || x.Symbols == 0 {
		t.Errorf("%d symbols holding %d bytes, want some within the budget", x.Symbols, total)
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
