package control

import (
	"slices"
	"strings"
	"testing"

	"gitbay.org/gitbay/internal/protocol"
	"gitbay.org/gitbay/internal/store"
)

func TestListsPlainPinned(t *testing.T) {
	st, repo, owner := twoMRTestRepo(t)
	if _, err := st.CreateIssue(repo.ID, owner.ID, "Android app", "", "md"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateBuild(repo.ID, "test", "abc111", "main", `["go test ./..."]`, "", "", true); err != nil {
		t.Fatal(err)
	}
	for name, argv := range map[string][]string{
		"mr-list":    {"mr", "list", repo.Path()},
		"issue-list": {"issue", "list", repo.Path()},
		"build-list": {"build", "list", repo.Path()},
	} {
		c, out, errOut := mrTestCtx(st, owner)
		if code := Dispatch(c, argv); code != protocol.ExitOK {
			t.Fatalf("%s: exit %d: %s", name, code, errOut)
		}
		pinPlain(t, name, out.String())
	}
}

func TestMRListScreen(t *testing.T) {
	c := screenCtx(100, false)
	c.User = store.User{Username: "alice"}
	mrs := []store.MR{{ID: 1, Number: 552, UpdatedAt: "2026-10-01T11:40:00Z"}, {ID: 2, Number: 551, UpdatedAt: "2026-10-01T10:00:00Z"}}
	ds := []mrOut{
		{Number: 552, Title: "wire $PAGER", State: "open", Source: "cli-pager", TargetRef: "main"},
		{Number: 551, Title: "changelog", State: "open", Source: "release", TargetRef: "stable"},
	}
	checks := map[int64]cell{1: cMark("✓ 2/2", sgrGreen), 2: cMark("✗ 1 failed", sgrRed)}
	review := map[int64]cell{1: cMark("review requested", sgrYellow)}
	s := mrListScreen(c, screenRepo, "open", mrs, ds, checks, review)
	if len(s.sections) != 1 || s.sections[0].title != "Open merge requests" || s.sections[0].n != 2 {
		t.Fatalf("sections = %+v", s.sections)
	}
	if g := s.sections[0].rows[0].cells[1]; g.s != "●" {
		t.Errorf("review requested of the viewer should lead with ●, got %q", g.s)
	}
	if g := s.sections[0].rows[1].cells[1]; g.s != "✗" || g.sgr != sgrRed {
		t.Errorf("failed checks should lead with a red ✗, got %+v", g)
	}
	out := renderString(c, s)
	if !strings.Contains(out, "release → stable") {
		t.Errorf("non-default target not shown:\n%s", out)
	}
	var verbs []string
	for _, a := range s.actions {
		verbs = append(verbs, strings.Join(a.argv[:2], " "))
	}
	if !slices.Equal(verbs, []string{"mr create", "mr list"}) {
		t.Errorf("actions = %q", verbs)
	}
	checkActions(t, s)
}

func TestIssueListScreen(t *testing.T) {
	c := screenCtx(100, false)
	c.User = store.User{Username: "alice"}
	issues := []store.Issue{{ID: 1, Number: 12, UpdatedAt: "2026-09-01T10:00:00Z"}, {ID: 2, Number: 13, UpdatedAt: "2026-09-02T10:00:00Z"}}
	ds := []issueOut{{Number: 12, Title: "Android app", State: "open"}, {Number: 13, Title: "Docs", State: "open"}}
	s := issueListScreen(c, screenRepo, "open", issues, ds, map[int64]int{1: 3}, map[int64][]string{1: {"mobile"}}, map[int64][]string{1: {"alice"}})
	sec := s.sections[0]
	if sec.title != "Open issues" || sec.n != 2 {
		t.Fatalf("section = %+v", sec)
	}
	if g := sec.rows[0].cells[1]; g.s != "●" {
		t.Errorf("assigned to the viewer should lead with ●, got %q", g.s)
	}
	if out := renderString(c, s); !strings.Contains(out, "mobile · alice · 3 comments") {
		t.Errorf("meta:\n%s", out)
	}
	var verbs []string
	for _, a := range s.actions {
		verbs = append(verbs, strings.Join(a.argv, " "))
	}
	if !slices.Equal(verbs, []string{"issue create krz/gitbay", "issue list krz/gitbay --state closed"}) {
		t.Errorf("actions = %q", verbs)
	}
	checkActions(t, s)
}

func TestBuildListScreen(t *testing.T) {
	c := screenCtx(100, false)
	ds := []BuildOut{
		{Number: 1784, Job: "test", Status: "running", Ref: "cli-views-2a", Subject: "docs: screens", CreatedAt: "2026-10-01T12:00:00Z"},
		{Number: 1783, Job: "build", Status: "success", Ref: "cli-views-2a", Subject: "docs: screens", CreatedAt: "2026-10-01T12:00:00Z"},
	}
	s := buildListScreen(c, screenRepo, ds)
	sec := s.sections[0]
	if sec.title != "Builds" || sec.n != 2 {
		t.Fatalf("section = %+v", sec)
	}
	if g := sec.rows[0].cells[1]; g.s != "◐" {
		t.Errorf("running build glyph = %q", g.s)
	}
	if len(s.actions) != 1 || strings.Join(s.actions[0].argv, " ") != "build show krz/gitbay 1784" {
		t.Errorf("actions = %+v", s.actions)
	}
	checkActions(t, s)
}
