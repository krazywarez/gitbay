package store

import (
	"strings"
	"testing"
)

func symbolFixture(t *testing.T) (*Store, int64) {
	t.Helper()
	s := open(t)
	if err := s.MigrateUp(); err != nil {
		t.Fatal(err)
	}
	uid, err := s.CreateUser("alice", false)
	if err != nil {
		t.Fatal(err)
	}
	repoID, err := s.CreateRepo("user", uid, "app", "public")
	if err != nil {
		t.Fatal(err)
	}
	idx, err := s.ReplaceSymbolIndex(SymbolIndex{RepoID: repoID, Commit: "c", Tree: "t", State: "ok"}, []SymbolRow{
		{Name: "handlers", Key: "handlers", Kind: "var", Path: "b.go", Line: 1},
		{Name: "Server.Handle", Key: "Handle", Kind: "method", Path: "a.go", Line: 9},
		{Name: "Handle", Key: "Handle", Kind: "function", Path: "z.go", Line: 3},
		{Name: "handle", Key: "handle", Kind: "function", Path: "c.go", Line: 2},
		{Name: "HandleFunc", Key: "HandleFunc", Kind: "function", Path: "a.go", Line: 20},
		{Name: "Other", Key: "Other", Kind: "type", Path: "a.go", Line: 30},
	})
	if err != nil {
		t.Fatal(err)
	}
	return s, idx
}

func symbolNames(rows []SymbolRow) string {
	var out []string
	for _, r := range rows {
		out = append(out, r.Name)
	}
	return strings.Join(out, " ")
}

// Exact before prefix, case-sensitive before not, then by name; a method
// matches on the name it is called by as well as Type.Method.
func TestSearchSymbolsRanks(t *testing.T) {
	s, idx := symbolFixture(t)
	rows, err := s.SearchSymbols(idx, "Handle", "", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := symbolNames(rows), "Handle Server.Handle HandleFunc handle handlers"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	rows, _ = s.SearchSymbols(idx, "Handle", "method", 0, 0)
	if got := symbolNames(rows); got != "Server.Handle" {
		t.Fatalf("kind filter: %q", got)
	}
	rows, _ = s.SearchSymbols(idx, "server.", "", 0, 0)
	if got := symbolNames(rows); got != "Server.Handle" {
		t.Fatalf("Type. prefix: %q", got)
	}

	// Paging walks the same order without repeats.
	var paged []SymbolRow
	var after int64
	for {
		page, err := s.SearchSymbols(idx, "handle", "", 2, after)
		if err != nil {
			t.Fatal(err)
		}
		if len(page) == 0 {
			break
		}
		paged = append(paged, page...)
		after = page[len(page)-1].ID
	}
	all, _ := s.SearchSymbols(idx, "handle", "", 0, 0)
	if symbolNames(paged) != symbolNames(all) || len(all) != 5 {
		t.Fatalf("paged %q, all %q", symbolNames(paged), symbolNames(all))
	}
}

func TestSymbolTargets(t *testing.T) {
	s, idx := symbolFixture(t)
	got, err := s.SymbolTargets(idx, []string{"Handle", "Other", "missing"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got["Handle"].Count != 2 || got["Other"] != (SymbolTarget{1, "a.go", 30}) {
		t.Fatalf("targets = %+v", got)
	}
}
