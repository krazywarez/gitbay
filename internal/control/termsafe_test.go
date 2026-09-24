package control

import (
	"bytes"
	"strings"
	"testing"

	"gitbay.org/gitbay/internal/protocol"
	"gitbay.org/gitbay/internal/store"
)

const evil = "x\x1b]52;c;ZXZpbA==\x07y\x1b[2Jz"

func TestTermSafe(t *testing.T) {
	cases := map[string]string{
		"plain\ttext\n":   "plain\ttext\n",
		"a\x1bb":          "a�b",
		"a\x00c\x7fd":     "a�c�d",
		"a\r\nb\r\n":      "a\nb\n",
		"a\rb":            "ab",
		"a\u0085b\u009bc": "a�b�c",
		"ümlaut":          "ümlaut",
	}
	for in, want := range cases {
		if got := termSafe(in); got != want {
			t.Errorf("termSafe(%q) = %q, want %q", in, got, want)
		}
	}
}

func noControls(t *testing.T, what, out string) {
	t.Helper()
	s := stripSGR(out)
	if strings.ContainsAny(s, "\x1b\x07") {
		t.Errorf("%s: control bytes reach the terminal: %q", what, s)
	}
}

func TestTableRowControlBytes(t *testing.T) {
	var plain, term bytes.Buffer
	for _, c := range []struct {
		ctx *Ctx
		w   *bytes.Buffer
	}{{&Ctx{}, &plain}, {&Ctx{Term: Term{Cols: 80, Color: true}}, &term}} {
		tb := c.ctx.table(c.w, "#", "STATE", "TITLE")
		tb.row(cRef("#1"), cState("open"), cFlex(evil))
		tb.flush()
	}
	noControls(t, "table", term.String())
	if want := "#1\topen\t" + evil + "\n"; plain.String() != want {
		t.Errorf("plain changed: %q", plain.String())
	}
}

func TestIssueShowControlBytes(t *testing.T) {
	show := func(term Term) string {
		st, repo, uid := newQueueTestRepo(t)
		if _, err := st.CreateIssue(repo.ID, uid, evil, "body "+evil, "md"); err != nil {
			t.Fatal(err)
		}
		c, errOut := pruneCtx(st, t.TempDir(), store.User{ID: uid, Username: "alice"})
		c.Term = term
		if code := Dispatch(c, []string{"issue", "show", repo.Path(), "1"}); code != protocol.ExitOK {
			t.Fatalf("exit %d: %s", code, errOut)
		}
		return c.Stdout.(*bytes.Buffer).String()
	}
	noControls(t, "issue show", show(Term{Cols: 80, Color: true}))
	if out := show(Term{}); !strings.Contains(out, "#1  "+evil+"  open\n") {
		t.Errorf("plain title changed:\n%q", out)
	}
}

// A body written in a browser arrives with CRLF line endings.
func TestIssueShowCRLFBody(t *testing.T) {
	st, repo, uid := newQueueTestRepo(t)
	if _, err := st.CreateIssue(repo.ID, uid, "t", "first line\r\nsecond line\r\n", "md"); err != nil {
		t.Fatal(err)
	}
	c, errOut := pruneCtx(st, t.TempDir(), store.User{ID: uid, Username: "alice"})
	c.Term = Term{Cols: 80}
	if code := Dispatch(c, []string{"issue", "show", repo.Path(), "1"}); code != protocol.ExitOK {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	out := c.Stdout.(*bytes.Buffer).String()
	if strings.ContainsAny(out, "\r�") || !strings.Contains(out, "first line second line") {
		t.Errorf("CRLF body at a terminal:\n%q", out)
	}
}
