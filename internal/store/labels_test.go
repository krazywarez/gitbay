package store

import (
	"errors"
	"testing"
)

// acmeFixture: org acme owned by alice with repos acme/core and
// acme/site, an issue in each, and alice's own alice/app.
type acmeFixture struct {
	s          *Store
	alice      int64
	org        int64
	core, site Repo
	app        Repo
	coreIssue  int64
	siteIssue  int64
}

func newAcme(t *testing.T) acmeFixture {
	t.Helper()
	s := open(t)
	if err := s.MigrateUp(); err != nil {
		t.Fatal(err)
	}
	var f acmeFixture
	f.s = s
	var err error
	if f.alice, err = s.CreateUser("alice", false); err != nil {
		t.Fatal(err)
	}
	if f.org, err = s.CreateOrg("acme", f.alice); err != nil {
		t.Fatal(err)
	}
	mk := func(kind string, owner int64, name string) Repo {
		id, err := s.CreateRepo(kind, owner, name, "public")
		if err != nil {
			t.Fatal(err)
		}
		r, err := s.RepoByID(id)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	f.core = mk("org", f.org, "core")
	f.site = mk("org", f.org, "site")
	f.app = mk("user", f.alice, "app")
	// CreateIssue returns the per-repo issue number, not the issues.id row
	// that issue_labels.issue_id references (and that every production
	// caller of SetIssueLabel passes); resolve it the same way they do, or
	// core's and site's both-numbered-1 first issues collide.
	mkIssue := func(repo Repo, title string) int64 {
		n, err := s.CreateIssue(repo.ID, f.alice, title, "", "md")
		if err != nil {
			t.Fatal(err)
		}
		iss, err := s.IssueByNumber(repo.ID, n)
		if err != nil {
			t.Fatal(err)
		}
		return iss.ID
	}
	f.coreIssue = mkIssue(f.core, "c1")
	f.siteIssue = mkIssue(f.site, "s1")
	return f
}

func (f acmeFixture) orgRepos() []int64 { return []int64{f.core.ID, f.site.ID} }

func TestOrgLabelSeenByEveryOrgRepo(t *testing.T) {
	f := newAcme(t)
	if _, err := f.s.SetOrgLabel(f.org, "bug", "#ff0000"); err != nil {
		t.Fatal(err)
	}
	if err := f.s.SetLabel(f.site, "docs", ""); err != nil {
		t.Fatal(err)
	}
	// site sees the org's bug first, then its own docs; core sees only bug;
	// alice/app, user-owned, sees nothing.
	got, err := f.s.ListLabels(f.site, f.orgRepos())
	if err != nil || len(got) != 2 || got[0].Name != "bug" || !got[0].Org || got[1].Name != "docs" || got[1].Org {
		t.Fatalf("site labels = %+v, %v", got, err)
	}
	if got, _ := f.s.ListLabels(f.core, f.orgRepos()); len(got) != 1 || got[0].Name != "bug" {
		t.Fatalf("core labels = %+v", got)
	}
	if got, _ := f.s.ListLabels(f.app, []int64{f.app.ID}); len(got) != 0 {
		t.Fatalf("app labels = %+v", got)
	}
	colors, _ := f.s.LabelColors(f.core)
	if colors["bug"] != "#ff0000" {
		t.Fatalf("core colours = %v", colors)
	}
}

func TestIssueLabelResolvesOrgRowFirst(t *testing.T) {
	f := newAcme(t)
	if _, err := f.s.SetOrgLabel(f.org, "bug", ""); err != nil {
		t.Fatal(err)
	}
	if err := f.s.SetIssueLabel(f.core, f.coreIssue, "bug", true); err != nil {
		t.Fatal(err)
	}
	if err := f.s.SetIssueLabel(f.site, f.siteIssue, "bug", true); err != nil {
		t.Fatal(err)
	}
	// One org row, no repo rows were created on the fly.
	var n int
	f.s.DB.QueryRow("SELECT COUNT(*) FROM labels WHERE name = 'bug'").Scan(&n)
	if n != 1 {
		t.Fatalf("labels named bug: %d, want 1", n)
	}
	// The count spans the org's readable repos.
	got, _ := f.s.ListOrgLabels(f.org, f.orgRepos())
	if len(got) != 1 || got[0].Issues != 2 {
		t.Fatalf("org labels = %+v", got)
	}
	got, _ = f.s.ListOrgLabels(f.org, []int64{f.core.ID})
	if got[0].Issues != 1 {
		t.Fatalf("org labels over core only = %+v", got)
	}
	// A label neither scope has is still created on the fly in the repo.
	if err := f.s.SetIssueLabel(f.core, f.coreIssue, "adhoc", true); err != nil {
		t.Fatal(err)
	}
	if l, err := f.s.LabelByName(f.core, "adhoc"); err != nil || l.Org {
		t.Fatalf("adhoc = %+v, %v", l, err)
	}
	// Removing by name works for the org row too.
	if err := f.s.SetIssueLabel(f.core, f.coreIssue, "bug", false); err != nil {
		t.Fatal(err)
	}
	got, _ = f.s.ListOrgLabels(f.org, f.orgRepos())
	if got[0].Issues != 1 {
		t.Fatalf("after detach: %+v", got)
	}
}

// The web issue list reads labels per repository; an org label attached
// to an issue has to come back from there like the repository's own.
func TestListIssueLabelsIncludesOrgRows(t *testing.T) {
	f := newAcme(t)
	if _, err := f.s.SetOrgLabel(f.org, "bug", ""); err != nil {
		t.Fatal(err)
	}
	if err := f.s.SetLabel(f.core, "docs", ""); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"bug", "docs"} {
		if err := f.s.SetIssueLabel(f.core, f.coreIssue, name, true); err != nil {
			t.Fatal(err)
		}
	}
	got, err := f.s.ListIssueLabels(f.core)
	if err != nil || len(got[f.coreIssue]) != 2 || got[f.coreIssue][0] != "bug" || got[f.coreIssue][1] != "docs" {
		t.Fatalf("core issue labels = %v, %v", got, err)
	}
	// Another repository under the org does not pick up core's attachment.
	if got, _ := f.s.ListIssueLabels(f.site); len(got) != 0 {
		t.Fatalf("site issue labels = %v", got)
	}
}

func TestRepoLabelRefusedWhenOrgHoldsName(t *testing.T) {
	f := newAcme(t)
	if _, err := f.s.SetOrgLabel(f.org, "bug", ""); err != nil {
		t.Fatal(err)
	}
	if err := f.s.SetLabel(f.core, "bug", "#00ff00"); !errors.Is(err, ErrOrgScoped) {
		t.Fatalf("SetLabel over org name: %v, want ErrOrgScoped", err)
	}
	if err := f.s.DeleteLabel(f.core, "bug"); !errors.Is(err, ErrOrgScoped) {
		t.Fatalf("DeleteLabel of org row: %v, want ErrOrgScoped", err)
	}
	if err := f.s.DeleteLabel(f.core, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("DeleteLabel of nothing: %v, want ErrNotFound", err)
	}
	// A user-owned repo is unaffected by any org.
	if err := f.s.SetLabel(f.app, "bug", ""); err != nil {
		t.Fatal(err)
	}
}

func TestSetOrgLabelPromotesRepoLabels(t *testing.T) {
	f := newAcme(t)
	if err := f.s.SetIssueLabel(f.core, f.coreIssue, "bug", true); err != nil {
		t.Fatal(err)
	}
	if err := f.s.SetIssueLabel(f.site, f.siteIssue, "bug", true); err != nil {
		t.Fatal(err)
	}
	if err := f.s.SetLabel(f.app, "bug", "#123456"); err != nil {
		t.Fatal(err)
	}
	folded, err := f.s.SetOrgLabel(f.org, "bug", "#ff0000")
	if err != nil || folded != 2 {
		t.Fatalf("SetOrgLabel folded %d, %v; want 2", folded, err)
	}
	var n int
	f.s.DB.QueryRow("SELECT COUNT(*) FROM labels WHERE name = 'bug' AND org_id = ?", f.org).Scan(&n)
	if n != 1 {
		t.Fatalf("org rows named bug: %d", n)
	}
	f.s.DB.QueryRow("SELECT COUNT(*) FROM labels WHERE name = 'bug' AND repo_id IN (?, ?)", f.core.ID, f.site.ID).Scan(&n)
	if n != 0 {
		t.Fatalf("repo rows named bug left under the org: %d", n)
	}
	got, _ := f.s.ListOrgLabels(f.org, f.orgRepos())
	if len(got) != 1 || got[0].Issues != 2 || got[0].Color != "#ff0000" {
		t.Fatalf("after promote: %+v", got)
	}
	// alice/app's own bug is another owner's and stays.
	if l, err := f.s.LabelByName(f.app, "bug"); err != nil || l.Color != "#123456" {
		t.Fatalf("app bug = %+v, %v", l, err)
	}
	// A second set only recolours.
	if folded, err := f.s.SetOrgLabel(f.org, "bug", "#0000ff"); err != nil || folded != 0 {
		t.Fatalf("second set folded %d, %v", folded, err)
	}
	if err := f.s.DeleteOrgLabel(f.org, "bug"); err != nil {
		t.Fatal(err)
	}
	if err := f.s.DeleteOrgLabel(f.org, "bug"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete: %v", err)
	}
	f.s.DB.QueryRow("SELECT COUNT(*) FROM issue_labels").Scan(&n)
	if n != 0 {
		t.Fatalf("memberships after org delete: %d", n)
	}
}
