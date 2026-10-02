package control

import (
	"slices"
	"testing"

	"gitbay.org/gitbay/internal/protocol"
	"gitbay.org/gitbay/internal/store"
)

func TestIssueShowPlainPinned(t *testing.T) {
	st, repo, uid := newQueueTestRepo(t)
	owner := store.User{ID: uid, Username: "alice"}
	id, err := st.CreateIssue(repo.ID, uid, "Android app", "An app.", "md")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.AddIssueComment(id, uid, "Started.", "md"); err != nil {
		t.Fatal(err)
	}
	c, out, errOut := mrTestCtx(st, owner)
	if code := Dispatch(c, []string{"issue", "show", repo.Path(), "1"}); code != protocol.ExitOK {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	pinPlain(t, "issue-show", out.String())
}

func issueFixture() IssueShow {
	return IssueShow{issueOut: issueOut{Number: 12, Title: "Android app", State: "open", Author: "cmc",
		Labels: []string{"mobile"}, Assignees: []string{"alice"}, Body: "An app.", BodyFormat: "md",
		CreatedAt: "2026-08-21T10:00:00Z"},
		Comments: []commentOut{
			{ID: 1, Author: "cmc", Body: "Started.", BodyFormat: "md", CreatedAt: "2026-09-01T10:00:00Z", Kind: "comment"},
			{ID: 2, Author: "cmc", Body: "labelled mobile", CreatedAt: "2026-09-01T10:00:00Z", Kind: "system"},
		}}
}

func actionVerbs(s screen) []string {
	var v []string
	for _, a := range s.actions {
		v = append(v, a.argv[1])
	}
	return v
}

func TestIssueShowScreenWriter(t *testing.T) {
	c := screenCtx(100, false)
	c.User = store.User{Username: "alice"}
	s := issueShowScreen(c, screenRepo, issueFixture(), true)
	if n := sectionCounts(s); n["Discussion"] != 1 || n["Events"] != 1 {
		t.Errorf("sections = %v", n)
	}
	if got, want := actionVerbs(s), []string{"comment", "assign", "label", "close"}; !slices.Equal(got, want) {
		t.Errorf("actions = %q, want %q", got, want)
	}
	checkActions(t, s)
}

func TestIssueShowScreenAuthorCanClose(t *testing.T) {
	c := screenCtx(100, false)
	c.User = store.User{Username: "cmc"}
	s := issueShowScreen(c, screenRepo, issueFixture(), false)
	if got, want := actionVerbs(s), []string{"comment", "close"}; !slices.Equal(got, want) {
		t.Errorf("actions = %q, want %q", got, want)
	}
}

func TestIssueShowScreenReader(t *testing.T) {
	c := screenCtx(100, false)
	c.User = store.User{Username: "bob"}
	d := issueFixture()
	d.State = "closed"
	s := issueShowScreen(c, screenRepo, d, false)
	if got, want := actionVerbs(s), []string{"comment"}; !slices.Equal(got, want) {
		t.Errorf("actions = %q, want %q", got, want)
	}
}
