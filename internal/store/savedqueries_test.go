package store

import (
	"errors"
	"fmt"
	"testing"
)

type queryFixture struct {
	s                  *Store
	alice, bob, carol  int64
	pub, priv, bobPriv Repo
}

// newQueryFixture: alice owns a public and a private repository, bob a
// private one; carol owns nothing and is granted nothing.
func newQueryFixture(t *testing.T) queryFixture {
	t.Helper()
	s := open(t)
	if err := s.MigrateUp(); err != nil {
		t.Fatal(err)
	}
	var f queryFixture
	f.s = s
	must := func(id int64, err error) int64 {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	f.alice = must(s.CreateUser("alice", false))
	f.bob = must(s.CreateUser("bob", false))
	f.carol = must(s.CreateUser("carol", false))
	repo := func(owner int64, name, vis string) Repo {
		r, err := s.RepoByID(must(s.CreateRepo("user", owner, name, vis)))
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	f.pub = repo(f.alice, "pub", "public")
	f.priv = repo(f.alice, "priv", "private")
	f.bobPriv = repo(f.bob, "secret", "private")
	return f
}

func (f queryFixture) issue(t *testing.T, r Repo, author int64, title string) Issue {
	t.Helper()
	n, err := f.s.CreateIssue(r.ID, author, title, "", "md")
	if err != nil {
		t.Fatal(err)
	}
	i, err := f.s.IssueByNumber(r.ID, n)
	if err != nil {
		t.Fatal(err)
	}
	return i
}

func (f queryFixture) mr(t *testing.T, r Repo, author int64, title string) MR {
	t.Helper()
	n, err := f.s.CreateMR(r.ID, author, r.ID, "topic", "main", title, "", "", "md", false)
	if err != nil {
		t.Fatal(err)
	}
	m, err := f.s.MRByNumber(r.ID, n)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func titles(items []Item) []string {
	var out []string
	for _, it := range items {
		out = append(out, it.Title)
	}
	return out
}

// A query reaches only what the caller may read: someone else's private
// repository is absent from the rows and from the count, even when the
// query names it.
func TestQueryItemsSkipsUnreadableRepositories(t *testing.T) {
	f := newQueryFixture(t)
	f.issue(t, f.pub, f.alice, "public bug")
	f.issue(t, f.priv, f.alice, "alice private bug")
	f.issue(t, f.bobPriv, f.bob, "bob private bug")
	f.mr(t, f.bobPriv, f.bob, "bob private mr")
	f.mr(t, f.pub, f.alice, "public mr")

	all := ItemFilter{Issues: true, MRs: true}
	for _, tc := range []struct {
		name string
		user int64
		f    ItemFilter
		want int
	}{
		{"carol, everything", f.carol, all, 2},
		{"alice, everything", f.alice, all, 3},
		{"bob, everything", f.bob, all, 4},
		{"carol naming bob's repository", f.carol, ItemFilter{Issues: true, MRs: true, Scopes: []RepoScope{{Owner: "bob", Name: "secret"}}}, 0},
		{"carol naming bob", f.carol, ItemFilter{Issues: true, MRs: true, Scopes: []RepoScope{{Owner: "bob"}}}, 0},
		{"carol globbing alice", f.carol, ItemFilter{Issues: true, MRs: true, Scopes: []RepoScope{{Owner: "alice", Name: "p*"}}}, 2},
		{"alice globbing herself", f.alice, ItemFilter{Issues: true, MRs: true, Scopes: []RepoScope{{Owner: "alice", Name: "p*"}}}, 3},
		{"carol by bob's text", f.carol, ItemFilter{Issues: true, MRs: true, Text: "bob"}, 0},
	} {
		items, err := f.s.QueryItems(tc.user, tc.f, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		n, err := f.s.CountItems(tc.user, tc.f)
		if err != nil {
			t.Fatal(err)
		}
		if len(items) != tc.want || n != tc.want {
			t.Errorf("%s: %d rows %q, count %d; want %d", tc.name, len(items), titles(items), n, tc.want)
		}
	}

	// A grant opens bob's repository to carol.
	if err := f.s.GrantAccess(f.bobPriv.ID, f.carol, "read"); err != nil {
		t.Fatal(err)
	}
	if n, _ := f.s.CountItems(f.carol, all); n != 4 {
		t.Errorf("carol with a grant counts %d, want 4", n)
	}
}

func TestQueryItemsFilters(t *testing.T) {
	f := newQueryFixture(t)
	bug := f.issue(t, f.pub, f.alice, "labelled bug")
	if err := f.s.SetIssueLabel(f.pub, bug.ID, "bug", true); err != nil {
		t.Fatal(err)
	}
	if err := f.s.SetIssueLabel(f.pub, bug.ID, "ui", true); err != nil {
		t.Fatal(err)
	}
	both := f.issue(t, f.pub, f.bob, "assigned in milestone")
	ms, err := f.s.CreateMilestone(f.pub, "v2", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.s.SetIssueMilestone(both.ID, ms); err != nil {
		t.Fatal(err)
	}
	if err := f.s.SetIssueAssignee(both.ID, f.carol, true); err != nil {
		t.Fatal(err)
	}
	closed := f.issue(t, f.pub, f.alice, "closed one")
	if err := f.s.SetIssueState(closed.ID, "closed"); err != nil {
		t.Fatal(err)
	}
	merged := f.mr(t, f.pub, f.alice, "merged mr")
	if err := f.s.MarkMerged(merged.ID, "", f.alice, ""); err != nil {
		t.Fatal(err)
	}
	f.mr(t, f.pub, f.bob, "open mr")

	for _, tc := range []struct {
		f    ItemFilter
		want string
	}{
		{ItemFilter{Issues: true, MRs: true, State: "open"}, "[open mr assigned in milestone labelled bug]"},
		{ItemFilter{Issues: true, MRs: true, State: "merged"}, "[merged mr]"},
		{ItemFilter{Issues: true, State: "closed"}, "[closed one]"},
		{ItemFilter{Issues: true, MRs: true, Labels: []string{"bug", "ui"}}, "[labelled bug]"},
		{ItemFilter{Issues: true, MRs: true, Labels: []string{"bug", "nope"}}, "[]"},
		{ItemFilter{Issues: true, NoLabel: true}, "[closed one assigned in milestone]"},
		{ItemFilter{Issues: true, Milestone: "v2"}, "[assigned in milestone]"},
		{ItemFilter{Issues: true, NoMilestone: true}, "[closed one labelled bug]"},
		{ItemFilter{Issues: true, Assignee: "carol"}, "[assigned in milestone]"},
		{ItemFilter{Issues: true, MRs: true, Author: "bob"}, "[open mr assigned in milestone]"},
		{ItemFilter{MRs: true}, "[open mr merged mr]"},
		{ItemFilter{}, "[]"},
	} {
		items, err := f.s.QueryItems(f.alice, tc.f, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		if got := fmt.Sprint(titles(items)); got != tc.want && !(got == "[]" && tc.want == "[]") {
			t.Errorf("%+v = %s, want %s", tc.f, got, tc.want)
		}
	}
}

// Paging walks every row once across repositories and both tables, even
// when rows share a creation time: the cursor carries the tie-breakers.
func TestQueryItemsPagesAcrossRepositories(t *testing.T) {
	f := newQueryFixture(t)
	var want []string
	for i := 0; i < 5; i++ {
		f.issue(t, f.pub, f.alice, fmt.Sprintf("pub issue %d", i))
		f.issue(t, f.priv, f.alice, fmt.Sprintf("priv issue %d", i))
		f.mr(t, f.pub, f.alice, fmt.Sprintf("pub mr %d", i))
		f.issue(t, f.bobPriv, f.bob, fmt.Sprintf("bob issue %d", i))
	}
	// Half the rows at one instant, so ordering falls to kind and id.
	if _, err := f.s.DB.Exec("UPDATE issues SET created_at = '2026-01-01T00:00:00.000Z' WHERE id % 2 = 0"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.DB.Exec("UPDATE merge_requests SET created_at = '2026-01-01T00:00:00.000Z' WHERE id % 2 = 1"); err != nil {
		t.Fatal(err)
	}
	all := ItemFilter{Issues: true, MRs: true}
	full, err := f.s.QueryItems(f.alice, all, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(full) != 15 {
		t.Fatalf("alice reads %d rows, want 15", len(full))
	}
	for _, it := range full {
		want = append(want, it.Kind+it.Title)
	}
	var got []string
	var after *ItemCursor
	for pages := 0; ; pages++ {
		if pages > 20 {
			t.Fatal("paging does not end")
		}
		page, err := f.s.QueryItems(f.alice, all, after, 4)
		if err != nil {
			t.Fatal(err)
		}
		for _, it := range page {
			got = append(got, it.Kind+it.Title)
		}
		if len(page) < 4 {
			break
		}
		c := page[len(page)-1].Cursor()
		after = &c
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("paged\n%v\nwant\n%v", got, want)
	}
}

func TestSavedQueries(t *testing.T) {
	f := newQueryFixture(t)
	s := f.s
	if err := s.SaveQuery(f.alice, "mine", "is:open", false); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveQuery(f.alice, "mine", "is:closed", false); !errors.Is(err, ErrExists) {
		t.Fatalf("second save without replace: %v, want ErrExists", err)
	}
	if err := s.PinSavedQuery(f.alice, "mine", true); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveQuery(f.alice, "mine", "is:closed", true); err != nil {
		t.Fatal(err)
	}
	q, err := s.SavedQueryByName(f.alice, "mine")
	if err != nil || q.Query != "is:closed" || !q.Pinned {
		t.Fatalf("after replace: %+v %v; want the new text, still pinned", q, err)
	}
	// Names are per user.
	if err := s.SaveQuery(f.bob, "mine", "is:open", false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SavedQueryByName(f.carol, "mine"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("carol sees alice's query: %v", err)
	}
	if err := s.SaveQuery(f.alice, "other", "is:open", false); err != nil {
		t.Fatal(err)
	}
	pinned, err := s.SavedQueries(f.alice, true)
	if err != nil || len(pinned) != 1 || pinned[0].Name != "mine" {
		t.Fatalf("pinned = %+v %v", pinned, err)
	}
	if err := s.RemoveSavedQuery(f.alice, "mine"); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveSavedQuery(f.alice, "mine"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second remove: %v", err)
	}
	if err := s.PinSavedQuery(f.alice, "gone", true); !errors.Is(err, ErrNotFound) {
		t.Fatalf("pin of a missing query: %v", err)
	}
}
