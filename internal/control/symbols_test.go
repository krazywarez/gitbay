package control

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"gitbay.org/gitbay/internal/protocol"
	"gitbay.org/gitbay/internal/store"
)

func symbolsFixture(t *testing.T) (*store.Store, store.Repo, store.User, store.User) {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.MigrateUp(); err != nil {
		t.Fatal(err)
	}
	aliceID, _ := st.CreateUser("alice", false)
	bobID, _ := st.CreateUser("bob", false)
	id, err := st.CreateRepo("user", aliceID, "secret", "private")
	if err != nil {
		t.Fatal(err)
	}
	repo, _ := st.RepoByID(id)
	var rows []store.SymbolRow
	for _, n := range []string{"Parse", "ParseAll", "ParseArgs", "Parser", "parse"} {
		rows = append(rows, store.SymbolRow{Name: n, Key: n, Kind: "function", Path: "p.go", Line: len(rows) + 1})
	}
	rows = append(rows, store.SymbolRow{Name: "Parser.Run", Key: "Run", Kind: "method", Path: "p.go", Line: 40})
	if _, err := st.ReplaceSymbolIndex(store.SymbolIndex{RepoID: id, Commit: "c0", Tree: "t0", State: "ok"}, rows); err != nil {
		t.Fatal(err)
	}
	return st, repo,
		store.User{ID: aliceID, Username: "alice"},
		store.User{ID: bobID, Username: "bob"}
}

type symbolPage struct {
	Data struct {
		Items []symbolOut `json:"items"`
		Next  string      `json:"next"`
	} `json:"data"`
}

func runSymbols(t *testing.T, st *store.Store, u store.User, argv ...string) (int, string, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	c := &Ctx{User: u, Scope: "full", Store: st, Stdout: &out, Stderr: &errOut, JSON: true}
	c.Cfg.Server.Root = t.TempDir()
	code := Dispatch(c, argv)
	if code != protocol.ExitOK {
		// In JSON mode a refusal is the output's error field.
		return code, out.String(), out.String() + errOut.String()
	}
	return code, out.String(), errOut.String()
}

func TestRepoSymbolsPages(t *testing.T) {
	st, repo, alice, _ := symbolsFixture(t)
	var names []string
	cursor := ""
	for pages := 0; ; pages++ {
		argv := []string{"repo", "symbols", repo.Path(), "Parse", "--limit", "2"}
		if cursor != "" {
			argv = append(argv, "--cursor", cursor)
		}
		code, out, errOut := runSymbols(t, st, alice, argv...)
		if code != protocol.ExitOK {
			t.Fatalf("exit %d: %s", code, errOut)
		}
		var p symbolPage
		if err := json.Unmarshal([]byte(out), &p); err != nil {
			t.Fatalf("%v: %s", err, out)
		}
		for _, it := range p.Data.Items {
			names = append(names, it.Name)
		}
		if p.Data.Next == "" {
			break
		}
		if pages > 5 {
			t.Fatal("paging does not end")
		}
		cursor = p.Data.Next
	}
	if got, want := strings.Join(names, " "), "Parse ParseAll ParseArgs Parser Parser.Run parse"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}

	// A cursor from before a rebuild is refused rather than followed.
	st.ReplaceSymbolIndex(store.SymbolIndex{RepoID: repo.ID, Commit: "c1", Tree: "t1", State: "ok"},
		[]store.SymbolRow{{Name: "Parse", Key: "Parse", Kind: "function", Path: "p.go", Line: 1}})
	code, _, errOut := runSymbols(t, st, alice, "repo", "symbols", repo.Path(), "Parse", "--cursor", cursor)
	if code != protocol.ExitUsage || !strings.Contains(errOut, "rebuilt") {
		t.Fatalf("stale cursor: exit %d: %s", code, errOut)
	}
}

func TestRepoSymbolsRefusals(t *testing.T) {
	st, repo, alice, bob := symbolsFixture(t)
	for _, tc := range []struct {
		name string
		user store.User
		argv []string
		code int
		msg  string
	}{
		{"outsider sees no repository", bob, []string{"repo", "symbols", repo.Path(), "Parse"}, protocol.ExitNotFound, "not found"},
		{"another ref", alice, []string{"repo", "symbols", repo.Path(), "--ref", "feature", "Parse"}, protocol.ExitNotFound, "only the default branch"},
		{"unknown kind", alice, []string{"repo", "symbols", repo.Path(), "--kind", "widget", "Parse"}, protocol.ExitUsage, "--kind"},
		{"no query", alice, []string{"repo", "symbols", repo.Path()}, protocol.ExitUsage, ""},
		{"one-character query", alice, []string{"repo", "symbols", repo.Path(), "P"}, protocol.ExitUsage, "2 to"},
		{"reindex needs an admin", alice, []string{"admin", "symbols", "reindex", repo.Path()}, protocol.ExitDenied, "admin"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, _, errOut := runSymbols(t, st, tc.user, tc.argv...)
			if code != tc.code || !strings.Contains(errOut, tc.msg) {
				t.Fatalf("exit %d, want %d with %q: %s", code, tc.code, tc.msg, errOut)
			}
		})
	}

	code, _, errOut := runSymbols(t, st, alice, "repo", "symbols", repo.Path(), "--ref", repo.DefaultBranch, "--kind", "method", "Run")
	if code != protocol.ExitOK {
		t.Fatalf("default branch by name: exit %d: %s", code, errOut)
	}
}

func TestAdminSymbolsReindexQueuesAForcedBuild(t *testing.T) {
	st, repo, _, _ := symbolsFixture(t)
	code, _, errOut := runSymbols(t, st, rootUser(t, st), "admin", "symbols", "reindex", repo.Path())
	if code != protocol.ExitOK {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	reqs, err := st.SymbolRequests()
	if err != nil || len(reqs) != 1 || reqs[0].RepoID != repo.ID || !reqs[0].Force {
		t.Fatalf("requests = %+v, %v", reqs, err)
	}
}
