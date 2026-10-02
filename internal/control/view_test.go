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
	if !strings.Contains(stripSGR(out), "referenced in commit abc1234567 by alice") {
		t.Errorf("event missing:\n%s", out)
	}
	for _, line := range strings.Split(stripSGR(out), "\n") {
		// A suggested command is never cut: it has to paste whole.
		if strings.HasPrefix(line, "gitbay ") {
			continue
		}
		if cells(line) > 60 {
			t.Errorf("line over 60 cells: %q", line)
		}
	}
}

// TestIssueShowNarrowWraps: a long title and long labels must not push
// any line past the terminal width, and no content is lost to it.
func TestIssueShowNarrowWraps(t *testing.T) {
	st, repo, uid := newQueueTestRepo(t)
	title := strings.TrimSpace(strings.Repeat("a very long issue title that keeps going on and on ", 3))[:90]
	n, err := st.CreateIssue(repo.ID, uid, title, "body", "md")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"needs-review-before-merge", "blocked-on-release", "requires-design-signoff"} {
		if err := st.SetLabel(repo, name, "888888"); err != nil {
			t.Fatal(err)
		}
		if err := st.SetIssueLabel(repo, issueIDFor(t, st, repo.ID, n), name, true); err != nil {
			t.Fatal(err)
		}
	}
	// No SiteURL set: the url field stays a short path so it does not
	// itself force an unbreakable line over width, which is not what
	// this test is about (title/labels/author wrapping is).
	c, errOut := pruneCtx(st, t.TempDir(), store.User{ID: uid, Username: "alice"})
	c.Term = Term{Cols: 40, Color: true}
	if code := Dispatch(c, []string{"issue", "show", repo.Path(), "1"}); code != protocol.ExitOK {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	out := c.Stdout.(*bytes.Buffer).String()

	var titleLines []string
	inTitle := false
	for _, line := range strings.Split(stripSGR(out), "\n") {
		// A suggested command is never cut: it has to paste whole.
		if cells(line) > 40 && !strings.HasPrefix(line, "gitbay ") {
			t.Errorf("line over 40 cells: %q", line)
		}
		_, rest, isTitle := strings.Cut(line, "#1  ")
		switch {
		case strings.HasPrefix(line, "Issue:") && isTitle:
			inTitle = true
			titleLines = append(titleLines, rest)
		case inTitle && strings.HasPrefix(line, "     "):
			titleLines = append(titleLines, strings.TrimSpace(line))
		default:
			inTitle = false
		}
	}
	got := strings.Join(strings.Fields(strings.Join(titleLines, " ")), " ")
	want := strings.Join(strings.Fields(title), " ")
	if got != want {
		t.Errorf("title text lost across wrap: got %q, want %q", got, want)
	}
	if n := strings.Count(stripSGR(out), "open"); n != 1 {
		t.Errorf("state word appears %d times, want 1:\n%s", n, out)
	}
}

func issueIDFor(t *testing.T, st *store.Store, repoID, n int64) int64 {
	t.Helper()
	issue, err := st.IssueByNumber(repoID, n)
	if err != nil {
		t.Fatal(err)
	}
	return issue.ID
}

// view is the piped writer: at a terminal it writes what it writes
// piped, so a caller that never migrated falls back to the plain layout.
func TestViewIgnoresTerminal(t *testing.T) {
	render := func(term Term) string {
		var b bytes.Buffer
		c := &Ctx{Term: term}
		v := c.view(&b)
		v.title("#1", "A title", "open")
		v.fields("author", "alice", "when", "2026-09-01T10:00:00Z")
		v.body("Some *body* text that is long enough to wrap at forty columns, if it wraps.", "md")
		v.section("files")
		v.event("labelled [bug](/x)", "md", "2026-09-01T10:00:00Z")
		v.comment(1, "bob", "2026-09-01T10:00:00Z", "A comment.", "md")
		return b.String()
	}
	if got, want := render(Term{Cols: 40, Color: true}), render(Term{}); got != want {
		t.Errorf("terminal view differs from piped:\n%s\n---\n%s", got, want)
	}
}
