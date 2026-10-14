package control

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gitbay.org/gitbay/internal/protocol"
	"gitbay.org/gitbay/internal/store"
)

// definitionsFixture is a repository whose default branch holds use.go,
// which calls names defined in lib.go, and a symbol index of that tree.
func definitionsFixture(t *testing.T) (*store.Store, store.Repo, store.User, string) {
	t.Helper()
	st, repo, uid := newQueueTestRepo(t)
	root := t.TempDir()
	git := gitRunner(t)
	src := filepath.Join(root, "src")
	git(root, "init", "-q", "-b", "main", "src")
	os.WriteFile(filepath.Join(src, "lib.go"), []byte("package app\n\nfunc Parse() {}\nfunc Run() {}\nfunc Run2() {}\n"), 0o644)
	os.WriteFile(filepath.Join(src, "use.go"), []byte("package app\n\nfunc main() {\n\tParse()\n\tRun()\n\tunknown()\n}\n"), 0o644)
	git(src, "add", ".")
	git(src, "commit", "-q", "-m", "base")
	dir := RepoDir(root, repo.OwnerName, repo.Name)
	os.MkdirAll(filepath.Dir(dir), 0o755)
	git(root, "clone", "-q", "--bare", src, dir)
	head := strings.TrimSpace(git(dir, "rev-parse", "main"))
	tree := strings.TrimSpace(git(dir, "rev-parse", "main^{tree}"))
	rows := []store.SymbolRow{
		{Name: "Parse", Key: "Parse", Kind: "function", Path: "lib.go", Line: 3},
		{Name: "Run", Key: "Run", Kind: "function", Path: "lib.go", Line: 4},
		{Name: "Server.Run", Key: "Run", Kind: "method", Path: "srv.go", Line: 9},
		{Name: "main", Key: "main", Kind: "function", Path: "use.go", Line: 3},
	}
	if _, err := st.ReplaceSymbolIndex(store.SymbolIndex{RepoID: repo.ID, Commit: head, Tree: tree, State: "ok"}, rows); err != nil {
		t.Fatal(err)
	}
	return st, repo, store.User{ID: uid, Username: "alice"}, root
}

func runDefinitions(t *testing.T, st *store.Store, u store.User, root string, argv ...string) (int, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	c := &Ctx{User: u, Scope: "full", Store: st, Stdout: &out, Stderr: &errOut, JSON: true}
	c.Cfg.Server.Root = root
	c.Cfg.Limits.MaxBlobBytes = 1 << 20
	code := Dispatch(c, argv)
	return code, out.String() + errOut.String()
}

// repo definitions says where each name a file uses is defined: the
// first definition and how many there are, so a client links a name
// defined once to its line and one defined more often to a search, the
// way the blob page does. Names the index does not hold are left out.
func TestRepoDefinitionsListsTheNamesAFileUses(t *testing.T) {
	st, repo, alice, root := definitionsFixture(t)

	code, out := runDefinitions(t, st, alice, root, "repo", "definitions", repo.Path(), "use.go")
	if code != protocol.ExitOK {
		t.Fatalf("exit %d: %s", code, out)
	}
	var env struct {
		Data []definitionOut `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatal(err)
	}
	want := []definitionOut{
		{Name: "Parse", Count: 1, Path: "lib.go", Line: 3},
		{Name: "Run", Count: 2, Path: "lib.go", Line: 4},
		{Name: "main", Count: 1, Path: "use.go", Line: 3},
	}
	if len(env.Data) != len(want) {
		t.Fatalf("got %+v", env.Data)
	}
	for i := range want {
		if env.Data[i] != want[i] {
			t.Errorf("row %d: got %+v, want %+v", i, env.Data[i], want[i])
		}
	}
}

func TestRepoDefinitionsRefusals(t *testing.T) {
	st, repo, alice, root := definitionsFixture(t)
	for _, tc := range []struct {
		argv []string
		code int
		want string
	}{
		{[]string{"repo", "definitions", repo.Path(), "nope.go"}, protocol.ExitNotFound, "no such file"},
		{[]string{"repo", "definitions", repo.Path(), "../x"}, protocol.ExitUsage, "inside the repository"},
		{[]string{"repo", "definitions", repo.Path(), "use.go", "--ref", "nope"}, protocol.ExitNotFound, "not at the indexed tree"},
	} {
		code, out := runDefinitions(t, st, alice, root, tc.argv...)
		if code != tc.code || !strings.Contains(out, tc.want) {
			t.Errorf("%v: exit %d, %s", tc.argv, code, out)
		}
	}
}

func TestRepoDefinitionsScreen(t *testing.T) {
	st, repo, alice, root := definitionsFixture(t)
	out := atTerminalIn(t, st, alice, root, "repo", "definitions", repo.Path(), "use.go")
	if !strings.Contains(out, "Parse") || !strings.Contains(out, "lib.go:4") {
		t.Fatalf("screen:\n%s", out)
	}
	checkLegend(t, out)
}
