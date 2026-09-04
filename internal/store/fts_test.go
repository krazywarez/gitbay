package store

import (
	"strings"
	"testing"
)

func TestFTSQuerySanitises(t *testing.T) {
	cases := map[string]string{
		"memory leak": `"memory" "leak"`,
		"c++":         `"c++"`,
		// Bare FTS5 operators are terms here, not syntax.
		"foo AND": `"foo" "AND"`,
		"a NOT b": `"a" "NOT" "b"`,
		// A double quote inside a phrase is escaped by doubling it.
		`say "hi"`:   `"say" """hi"""`,
		"  spaced  ": `"spaced"`,
		"":           `""`,
		"trailing -": `"trailing" "-"`,
		"col:on":     `"col:on"`,
	}
	for in, want := range cases {
		if got := FTSQuery(in); got != want {
			t.Errorf("FTSQuery(%q) = %q, want %q", in, got, want)
		}
	}
}

// Every one of these is a syntax error as a bare FTS5 expression. The
// search must return no rows, not fail.
func TestFTSQueryNeverErrors(t *testing.T) {
	s, repoID, _ := ftsFixture(t)
	for _, q := range []string{
		"c++", `"`, `""`, "AND", "NOT", "*", "-", "(", "a AND", "foo:", "^", "a OR",
	} {
		if _, err := s.QueryIssues(repoID, IssueFilter{State: "all", Search: q}); err != nil {
			t.Errorf("search %q failed: %v", q, err)
		}
	}
}

func ftsFixture(t *testing.T) (*Store, int64, int64) {
	t.Helper()
	s := open(t)
	if err := s.MigrateUp(); err != nil {
		t.Fatal(err)
	}
	uid, err := s.CreateUser("cmc", true)
	if err != nil {
		t.Fatal(err)
	}
	repoID, err := s.CreateRepo("user", uid, "lib", "public")
	if err != nil {
		t.Fatal(err)
	}
	return s, repoID, uid
}

func searchNumbers(t *testing.T, s *Store, repoID int64, q string) []int64 {
	t.Helper()
	got, err := s.QueryIssues(repoID, IssueFilter{State: "all", Search: q})
	if err != nil {
		t.Fatalf("search %q: %v", q, err)
	}
	var ns []int64
	for _, i := range got {
		ns = append(ns, i.Number)
	}
	return ns
}

func TestIssueSearchMatchesTitleAndBody(t *testing.T) {
	s, repoID, uid := ftsFixture(t)
	if _, err := s.CreateIssue(repoID, uid, "memory leak in the parser", "it climbs forever", "md"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateIssue(repoID, uid, "unrelated", "nothing to see", "md"); err != nil {
		t.Fatal(err)
	}

	if got := searchNumbers(t, s, repoID, "parser"); len(got) != 1 || got[0] != 1 {
		t.Fatalf("title match = %v", got)
	}
	// The body is the half a LIKE over titles could never reach, which is
	// the whole point of #114.
	if got := searchNumbers(t, s, repoID, "climbs"); len(got) != 1 || got[0] != 1 {
		t.Fatalf("body match = %v", got)
	}
	// Terms are ANDed.
	if got := searchNumbers(t, s, repoID, "memory nothing"); len(got) != 0 {
		t.Fatalf("terms are not ANDed: %v", got)
	}
	if got := searchNumbers(t, s, repoID, "MEMORY"); len(got) != 1 {
		t.Fatalf("search is case sensitive: %v", got)
	}
}

// An external-content FTS table is not maintained automatically: an edit
// or a delete leaves the old terms indexed unless the trigger removes
// them first. Stale terms are invisible until someone searches for a word
// that was deleted and gets a row that no longer says it.
func TestIssueSearchFollowsEdits(t *testing.T) {
	s, repoID, uid := ftsFixture(t)
	n, err := s.CreateIssue(repoID, uid, "original title", "original body", "md")
	if err != nil {
		t.Fatal(err)
	}
	issue, err := s.IssueByNumber(repoID, n)
	if err != nil {
		t.Fatal(err)
	}

	if got := searchNumbers(t, s, repoID, "original"); len(got) != 1 {
		t.Fatalf("fresh issue not indexed: %v", got)
	}
	title, body := "replaced title", "replaced body"
	if err := s.UpdateIssueText(issue.ID, &title, &body, nil); err != nil {
		t.Fatal(err)
	}
	if got := searchNumbers(t, s, repoID, "original"); len(got) != 0 {
		t.Fatalf("edited-away terms still match: %v", got)
	}
	if got := searchNumbers(t, s, repoID, "replaced"); len(got) != 1 {
		t.Fatalf("new terms not indexed: %v", got)
	}

	// Deleting the repository cascades to its issues; their terms must go
	// with them rather than pointing at rows that no longer exist.
	if err := s.DeleteRepo(repoID); err != nil {
		t.Fatal(err)
	}
	var n2 int
	if err := s.DB.QueryRow("SELECT count(*) FROM issue_fts WHERE issue_fts MATCH 'replaced'").Scan(&n2); err != nil {
		t.Fatal(err)
	}
	if n2 != 0 {
		t.Fatalf("%d index rows survived the delete", n2)
	}
}

// The migration backfills what was already in the database, since the
// triggers only see writes from their own creation onward.
func TestFTSBackfillsExistingRows(t *testing.T) {
	s := open(t)
	// Stop one short of the FTS migration, write rows the triggers cannot
	// have seen, then apply it.
	if err := s.MigrateTo(35); err != nil {
		t.Fatal(err)
	}
	uid, err := s.CreateUser("cmc", true)
	if err != nil {
		t.Fatal(err)
	}
	repoID, err := s.CreateRepo("user", uid, "lib", "public")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateIssue(repoID, uid, "older than the index", "prose from before", "md"); err != nil {
		t.Fatal(err)
	}
	if err := s.MigrateUp(); err != nil {
		t.Fatal(err)
	}
	if got := searchNumbers(t, s, repoID, "prose"); len(got) != 1 {
		t.Fatalf("pre-existing issue not backfilled: %v", got)
	}
}

func TestGlobalSearchReachesBodies(t *testing.T) {
	s, repoID, uid := ftsFixture(t)
	if _, err := s.CreateIssue(repoID, uid, "a title", "haystack needle haystack", "md"); err != nil {
		t.Fatal(err)
	}
	got, err := s.SearchIssues(uid, "needle", 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || !strings.Contains(got[0].RepoPath, "lib") {
		t.Fatalf("global body search = %+v", got)
	}
}
