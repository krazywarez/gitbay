package control

import (
	"bytes"
	"encoding/json"
	"testing"

	"gitbay.org/gitbay/internal/protocol"
	"gitbay.org/gitbay/internal/store"
)

// reactFixture is alice's repository with issue #1 and one comment on
// it, and a second user acting on the same store.
func reactFixture(t *testing.T, visibility string) (alice, bob *Ctx, repo store.Repo, commentID int64) {
	t.Helper()
	alice = notifTestCtx(t, "alice")
	repoID, err := alice.Store.CreateRepo("user", alice.User.ID, "app", visibility)
	if err != nil {
		t.Fatal(err)
	}
	repo, _ = alice.Store.RepoByID(repoID)
	if code := runIssueCreate(alice, []string{repo.Path(), "--title", "t", "--body", "b"}); code != 0 {
		t.Fatalf("create: %d", code)
	}
	if code := runIssueComment(alice, []string{repo.Path(), "1", "--message", "hi"}); code != 0 {
		t.Fatalf("comment: %d", code)
	}
	iss, _ := alice.Store.IssueByNumber(repo.ID, 1)
	cs, _ := alice.Store.ListIssueComments(iss.ID)
	commentID = cs[0].ID
	bobID, err := alice.Store.CreateUser("bob", false)
	if err != nil {
		t.Fatal(err)
	}
	b := *alice
	b.User = store.User{ID: bobID, Username: "bob"}
	b.Stdout, b.Stderr = &bytes.Buffer{}, &bytes.Buffer{}
	return alice, &b, repo, commentID
}

func showReactIssue(t *testing.T, c *Ctx, repo store.Repo) IssueShow {
	t.Helper()
	var out bytes.Buffer
	c.Stdout, c.JSON = &out, true
	defer func() { c.JSON = false }()
	if code := runIssueShow(c, []string{repo.Path(), "1"}); code != 0 {
		t.Fatalf("show: %d", code)
	}
	var env struct{ Data IssueShow }
	if err := json.Unmarshal(out.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	return env.Data
}

func TestIssueReact(t *testing.T) {
	alice, bob, repo, cid := reactFixture(t, "public")
	p := repo.Path()
	cmt := itoa(cid)

	for i := 0; i < 2; i++ { // twice is a no-op
		if code := runReact(bob, []string{p, "1", "+1"}, "issue"); code != 0 {
			t.Fatalf("react: %d", code)
		}
	}
	if code := runReact(alice, []string{p, "1", "👍"}, "issue"); code != 0 { // the emoji itself
		t.Fatalf("react emoji: %d", code)
	}
	if code := runReact(bob, []string{p, "1", "--comment", cmt, "hooray"}, "issue"); code != 0 {
		t.Fatalf("react comment: %d", code)
	}

	d := showReactIssue(t, bob, repo)
	if len(d.Reactions) != 1 || d.Reactions[0] != (ReactionOut{"+1", 2, true}) {
		t.Errorf("issue reactions as bob: %+v", d.Reactions)
	}
	if len(d.Comments) != 1 || d.Comments[0].ID != cid ||
		len(d.Comments[0].Reactions) != 1 || d.Comments[0].Reactions[0] != (ReactionOut{"hooray", 1, true}) {
		t.Errorf("comment reactions: %+v", d.Comments)
	}
	if d := showReactIssue(t, alice, repo); d.Comments[0].Reactions[0].Me || !d.Reactions[0].Me {
		t.Errorf("me flags as alice: %+v", d)
	}

	for i := 0; i < 2; i++ { // removing twice is a no-op
		if code := runReact(bob, []string{p, "1", "--remove", "+1"}, "issue"); code != 0 {
			t.Fatalf("remove: %d", code)
		}
	}
	if d := showReactIssue(t, alice, repo); len(d.Reactions) != 1 || d.Reactions[0].Count != 1 {
		t.Errorf("after remove: %+v", d.Reactions)
	}
	if code := runReact(bob, []string{p, "1", "--remove", "rocket"}, "issue"); code != 0 {
		t.Errorf("removing an absent reaction: %d", code)
	}

	// Reacting files nothing.
	var n int
	alice.Store.DB.QueryRow("SELECT COUNT(*) FROM events WHERE kind LIKE '%react%'").Scan(&n)
	if n != 0 {
		t.Errorf("%d events for reactions", n)
	}
	rows, _ := alice.Store.Inbox(alice.User.ID, false, 20, 0)
	if len(rows) != 0 {
		t.Errorf("alice notified: %+v", rows)
	}
}

func TestIssueReactRefusals(t *testing.T) {
	_, bob, repo, cid := reactFixture(t, "public")
	p := repo.Path()
	for name, tc := range map[string]struct {
		args []string
		want int
	}{
		"unknown reaction":   {[]string{p, "1", "thumbsup"}, protocol.ExitUsage},
		"missing reaction":   {[]string{p, "1"}, protocol.ExitUsage},
		"unknown comment":    {[]string{p, "1", "--comment", "999", "+1"}, protocol.ExitNotFound},
		"bad comment id":     {[]string{p, "1", "--comment", "x", "+1"}, protocol.ExitUsage},
		"unknown issue":      {[]string{p, "9", "+1"}, protocol.ExitNotFound},
		"unknown repository": {[]string{"alice/nope", "1", "+1"}, protocol.ExitNotFound},
	} {
		if code := runReact(bob, tc.args, "issue"); code != tc.want {
			t.Errorf("%s: exit %d, want %d", name, code, tc.want)
		}
	}
	_ = cid
}

func TestIssueReactPrivateIsNotFound(t *testing.T) {
	_, bob, repo, cid := reactFixture(t, "private")
	for _, args := range [][]string{
		{repo.Path(), "1", "+1"},
		{repo.Path(), "1", "--comment", itoa(cid), "+1"},
	} {
		if code := runReact(bob, args, "issue"); code != protocol.ExitNotFound {
			t.Errorf("%v: exit %d, want %d", args, code, protocol.ExitNotFound)
		}
	}
	if code := runIssueShow(bob, []string{repo.Path(), "1"}); code != protocol.ExitNotFound {
		t.Errorf("show: exit %d", code)
	}
}

func TestReactRefusedOnArchived(t *testing.T) {
	alice, bob, repo, _ := reactFixture(t, "public")
	if _, err := alice.Store.UpdateRepoSettings(repo.ID, func(s *store.RepoSettings) { s.Archived = true }); err != nil {
		t.Fatal(err)
	}
	if code := runReact(bob, []string{repo.Path(), "1", "+1"}, "issue"); code != protocol.ExitDenied {
		t.Errorf("exit %d, want %d", code, protocol.ExitDenied)
	}
}

func TestReactOnSystemCommentRefused(t *testing.T) {
	alice, _, repo, _ := reactFixture(t, "public")
	iss, _ := alice.Store.IssueByNumber(repo.ID, 1)
	alice.Store.AddIssueSystemComment(iss.ID, alice.User.ID, "closed")
	cs, _ := alice.Store.ListIssueComments(iss.ID)
	if code := runReact(alice, []string{repo.Path(), "1", "--comment", itoa(cs[1].ID), "+1"}, "issue"); code != protocol.ExitNotFound {
		t.Errorf("exit %d", code)
	}
}

func itoa(n int64) string { b, _ := json.Marshal(n); return string(b) }

func TestMRReact(t *testing.T) {
	alice, bob, repo, _ := reactFixture(t, "public")
	if _, err := alice.Store.CreateMR(repo.ID, alice.User.ID, repo.ID, "f", "main", "t", "", "abc", "md", false); err != nil {
		t.Fatal(err)
	}
	mr, _ := alice.Store.MRByNumber(repo.ID, 1)
	if err := alice.Store.AddMRComment(mr.ID, alice.User.ID, "hi", "md"); err != nil {
		t.Fatal(err)
	}
	cs, _ := alice.Store.ListMRComments(mr.ID)
	p := repo.Path()
	for _, args := range [][]string{{p, "1", "rocket"}, {p, "1", "--comment", itoa(cs[0].ID), "eyes"}} {
		if code := runReact(bob, args, "mr"); code != 0 {
			t.Fatalf("%v: %d", args, code)
		}
	}
	if code := runReact(bob, []string{p, "1", "--comment", "999", "eyes"}, "mr"); code != protocol.ExitNotFound {
		t.Errorf("unknown comment: %d", code)
	}
	got, _ := alice.Store.ReactionCounts("mr", mr.ID, bob.User.ID)
	if len(got[0]) != 1 || got[0][0].Reaction != "rocket" || !got[0][0].Me || got[cs[0].ID][0].Reaction != "eyes" {
		t.Errorf("counts: %+v", got)
	}
}

// A comment id that belongs to another thread, or to a repository the
// caller cannot read, is not found under a readable repository and
// issue or merge request, and nothing is stored.
func TestReactCommentFromElsewhereNotFound(t *testing.T) {
	alice, bob, repo, _ := reactFixture(t, "public")
	st := alice.Store

	// Another issue and MR in the same repository, each with a comment.
	if code := runIssueCreate(alice, []string{repo.Path(), "--title", "two"}); code != 0 {
		t.Fatal(code)
	}
	if code := runIssueComment(alice, []string{repo.Path(), "2", "--message", "other"}); code != 0 {
		t.Fatal(code)
	}
	iss2, _ := st.IssueByNumber(repo.ID, 2)
	ic2, _ := st.ListIssueComments(iss2.ID)
	for i := 0; i < 2; i++ {
		if _, err := st.CreateMR(repo.ID, alice.User.ID, repo.ID, "f", "main", "t", "", "abc", "md", false); err != nil {
			t.Fatal(err)
		}
	}
	mr1, _ := st.MRByNumber(repo.ID, 1)
	mr2, _ := st.MRByNumber(repo.ID, 2)
	st.AddMRComment(mr1.ID, alice.User.ID, "one", "md")
	st.AddMRComment(mr2.ID, alice.User.ID, "two", "md")
	mc1, _ := st.ListMRComments(mr1.ID)
	mc2, _ := st.ListMRComments(mr2.ID)

	// Another user's private repository, with an issue comment and an MR comment.
	carolID, _ := st.CreateUser("carol", false)
	pid, err := st.CreateRepo("user", carolID, "secret", "private")
	if err != nil {
		t.Fatal(err)
	}
	priv, _ := st.RepoByID(pid)
	carol := *alice
	carol.User = store.User{ID: carolID, Username: "carol"}
	carol.Stdout, carol.Stderr = &bytes.Buffer{}, &bytes.Buffer{}
	if code := runIssueCreate(&carol, []string{priv.Path(), "--title", "s"}); code != 0 {
		t.Fatal(code)
	}
	if code := runIssueComment(&carol, []string{priv.Path(), "1", "--message", "s"}); code != 0 {
		t.Fatal(code)
	}
	pi, _ := st.IssueByNumber(priv.ID, 1)
	pic, _ := st.ListIssueComments(pi.ID)
	if _, err := st.CreateMR(priv.ID, carolID, priv.ID, "f", "main", "t", "", "abc", "md", false); err != nil {
		t.Fatal(err)
	}
	pm, _ := st.MRByNumber(priv.ID, 1)
	st.AddMRComment(pm.ID, carolID, "s", "md")
	pmc, _ := st.ListMRComments(pm.ID)

	p := repo.Path()
	cases := []struct {
		noun string
		n    string
		id   int64
	}{
		{"issue", "1", ic2[0].ID},
		{"issue", "1", pic[0].ID},
		{"mr", "1", mc2[0].ID},
		{"mr", "1", pmc[0].ID},
	}
	_ = mc1
	for _, tc := range cases {
		if code := runReact(bob, []string{p, tc.n, "--comment", itoa(tc.id), "+1"}, tc.noun); code != protocol.ExitNotFound {
			t.Errorf("%s comment %d: exit %d, want %d", tc.noun, tc.id, code, protocol.ExitNotFound)
		}
	}
	for _, table := range []string{"issue_reactions", "mr_reactions"} {
		var n int
		st.DB.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n)
		if n != 0 {
			t.Errorf("%s holds %d rows", table, n)
		}
	}
}
