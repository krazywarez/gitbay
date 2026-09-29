package hookd

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"gitbay.org/gitbay/internal/control"
	"gitbay.org/gitbay/internal/policy"
	"gitbay.org/gitbay/internal/store"
	"gitbay.org/gitbay/internal/symbols"
)

// A push to the default branch asks for a symbol index and builds none
// itself; a push to another branch asks for nothing. The worker builds
// the index afterwards.
func TestPostReceiveRequestsSymbolIndex(t *testing.T) {
	f := newShapeFixture(t)
	requested := func() bool {
		t.Helper()
		reqs, err := f.st.SymbolRequests()
		if err != nil {
			t.Fatal(err)
		}
		return len(reqs) == 1 && reqs[0].RepoID == f.repo.ID && !reqs[0].Force
	}

	f.git(f.src, "checkout", "-q", "-b", "side")
	side := f.appCommit("side")
	f.sync()
	f.srv.postReceive(Request{RepoID: f.repo.ID, UserID: f.uid, Scope: "full", Updates: []policy.RefUpdate{
		{Ref: "refs/heads/side", Old: zeroSHA40, New: side}}})
	if requested() {
		t.Fatal("a push to a side branch asked for a symbol index")
	}

	f.git(f.src, "checkout", "-q", "main")
	os.WriteFile(filepath.Join(f.src, "main.go"), []byte("package main\n\nfunc Serve() {}\n"), 0o644)
	f.git(f.src, "add", ".")
	f.git(f.src, "commit", "-q", "-m", "go")
	head := f.sha("HEAD")
	f.sync()
	f.srv.postReceive(Request{RepoID: f.repo.ID, UserID: f.uid, Scope: "full", Updates: []policy.RefUpdate{
		{Ref: "refs/heads/main", Old: f.base, New: head}}})
	if !requested() {
		t.Fatal("a push to the default branch did not ask for a symbol index")
	}
	if _, err := f.st.SymbolIndexFor(f.repo.ID); err != store.ErrNotFound {
		t.Fatalf("the push built an index itself: %v", err)
	}

	w := symbols.New(f.st, func(owner, name string) string { return control.RepoDir(f.root, owner, name) })
	w.Sweep(context.Background())
	x, err := f.st.SymbolIndexFor(f.repo.ID)
	if err != nil || x.Commit != head || x.State != "ok" {
		t.Fatalf("index after the sweep = %+v, %v", x, err)
	}
	rows, _ := f.st.SearchSymbols(x.ID, "Serve", "", 0, 0)
	if len(rows) != 1 || rows[0].Path != "main.go" || rows[0].Line != 3 {
		t.Fatalf("Serve = %+v", rows)
	}
}
