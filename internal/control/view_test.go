package control

import (
	"bytes"
	"strings"
	"testing"

	"gitbay.org/gitbay/internal/protocol"
	"gitbay.org/gitbay/internal/store"
)

func showIssue(t *testing.T, term Term) string {
	t.Helper()
	st, repo, uid := newQueueTestRepo(t)
	n, err := st.CreateIssue(repo.ID, uid, "A title", "Body with a [link](/x/y).", "md")
	if err != nil {
		t.Fatal(err)
	}
	issue, err := st.IssueByNumber(repo.ID, n)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.AddIssueSystemComment(issue.ID, uid, "referenced in commit [abc1234567](/o/r/commit/abc) by [alice](/alice)"); err != nil {
		t.Fatal(err)
	}
	c, errOut := pruneCtx(st, t.TempDir(), store.User{ID: uid, Username: "alice"})
	c.Cfg.Server.SiteURL = "https://forge.test"
	c.Term = term
	if code := Dispatch(c, []string{"issue", "show", repo.Path(), "1"}); code != protocol.ExitOK {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	return c.Stdout.(*bytes.Buffer).String()
}

func TestIssueShowPlain(t *testing.T) {
	out := showIssue(t, Term{})
	for _, want := range []string{
		"#1  A title  open\n",
		"  author  alice, ",
		"  url     https://forge.test/",
		"Body with a link (https://forge.test/x/y).",
		"  · referenced in commit abc1234567 by alice  ",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "\x1b") || strings.Contains(out, "](") {
		t.Errorf("markup or SGR in plain view:\n%s", out)
	}
}

func TestIssueShowTerminal(t *testing.T) {
	out := showIssue(t, Term{Cols: 60, Color: true})
	if !strings.Contains(out, sgrGreen+"open"+sgrReset) {
		t.Errorf("state not coloured:\n%s", out)
	}
	if !strings.Contains(out, " UTC") {
		t.Errorf("no web-format timestamp:\n%s", out)
	}
	for _, line := range strings.Split(stripSGR(out), "\n") {
		if cells(line) > 60 {
			t.Errorf("line over 60 cells: %q", line)
		}
	}
}
