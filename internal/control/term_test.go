package control

import (
	"strings"
	"testing"
	"time"
)

func TestParseTerm(t *testing.T) {
	cases := map[string]Term{
		"120":                {Cols: 120},
		"120,color":          {Cols: 120, Color: true},
		"40":                 {Cols: 40},
		"39":                 {},
		"":                   {},
		"abc":                {},
		"80,blink":           {Cols: 80},
		"80,":                {Cols: 80},
		"80,truecolor,color": {Cols: 80, Color: true, TrueColor: true},
		"80,color,links":     {Cols: 80, Color: true, Links: true},
		"80,truecolor":       {Cols: 80},
		"abc,color":          {},
		"5000":               {},
	}
	for in, want := range cases {
		if got := ParseTerm(in); got != want {
			t.Errorf("ParseTerm(%q) = %+v, want %+v", in, got, want)
		}
	}
}

func TestCells(t *testing.T) {
	cases := map[string]int{
		"abc":                 3,
		"日本":                  4,
		"é":                   1,
		"é":                  1,
		"\x1b[32mopen\x1b[0m": 4,
		"":                    0,
	}
	for in, want := range cases {
		if got := cells(in); got != want {
			t.Errorf("cells(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestClip(t *testing.T) {
	if got := clip("Dependency updates available", 14); got != "Dependency up…" {
		t.Errorf("clip = %q", got)
	}
	if got := clip("short", 14); got != "short" {
		t.Errorf("clip = %q", got)
	}
	if got := clip("日本語のタイトル", 7); got != "日本語…" {
		t.Errorf("clip wide = %q", got)
	}
}

func TestStampAndRelAge(t *testing.T) {
	if got := stamp("2026-09-23T23:26:00.570Z"); got != "2026-09-23T23:26:00Z" {
		t.Errorf("stamp = %q", got)
	}
	if got := stamp("2026-09-23 23:26:00"); got != "2026-09-23T23:26:00Z" {
		t.Errorf("stamp sqlite = %q", got)
	}
	if got := stamp("garbage"); got != "garbage" {
		t.Errorf("stamp garbage = %q", got)
	}
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	cases := map[string]string{
		"2026-09-23T11:59:30Z": "just now",
		"2026-09-23T11:55:00Z": "5m ago",
		"2026-09-23T10:00:00Z": "2h ago",
		"2026-09-20T12:00:00Z": "3d ago",
		"2026-09-02T12:00:00Z": "3w ago",
		"2026-06-01T12:00:00Z": "2026-06-01",
		"2026-09-23T12:00:20Z": "just now",
		"2026-09-23T17:00:00Z": "in 5h",
		"2026-09-24T12:00:00Z": "in 1d",
		"2027-09-24T12:00:00Z": "2027-09-24",
		"not a time":           "not a time",
	}
	for in, want := range cases {
		if got := relAge(in, now); got != want {
			t.Errorf("relAge(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSizeAndDur(t *testing.T) {
	plain, term := Term{}, Term{Cols: 80}
	if got := plain.size(3177346); got != "3177346" {
		t.Errorf("plain size = %q", got)
	}
	if got := term.size(3177346); got != "3.0 MiB" {
		t.Errorf("term size = %q", got)
	}
	if got := plain.dur(3223); got != "3223s" {
		t.Errorf("plain dur = %q", got)
	}
	if got := term.dur(3223); got != "53m43s" {
		t.Errorf("term dur = %q", got)
	}
}

func TestPaintStateEachWord(t *testing.T) {
	got := Term{Cols: 80, Color: true}.paintState("private, archived")
	want := sgrRed + "private" + sgrReset + ", " + sgrDim + "archived" + sgrReset
	if got != want {
		t.Errorf("paintState = %q, want %q", got, want)
	}
	if got := (Term{Cols: 80}).paintState("private, archived"); got != "private, archived" {
		t.Errorf("no colour = %q", got)
	}
}

func TestHeading(t *testing.T) {
	if got := (Term{Cols: 80}).heading("waiting on your review:"); got != "Waiting on your review" {
		t.Errorf("heading = %q", got)
	}
}

func TestFailureAtTerminal(t *testing.T) {
	msg := "unknown flag \"--stat\"; did you mean --state?\nusage: gitbay issue list [<owner/name>] [--state open|closed|all] [--label <l>] [--assignee <user>]"
	got := Term{Cols: 50}.failure(msg)
	want := "error: unknown flag \"--stat\"; did you mean --state?\n" +
		"usage: gitbay issue list [<owner/name>]\n" +
		"       [--state open|closed|all] [--label <l>]\n" +
		"       [--assignee <user>]"
	if got != want {
		t.Errorf("failure:\n%s\nwant\n%s", got, want)
	}
	alt := Term{Cols: 80}.failure("usage: gitbay issue list [--limit <n>] | issue list --query <name> | --q <query>")
	if alt != "usage: gitbay issue list [--limit <n>]\n       | issue list --query <name>\n       | --q <query>" {
		t.Errorf("alternatives:\n%s", alt)
	}
	if got := (Term{Cols: 80}).failure("usage: gitbay mr merge <n>"); got != "usage: gitbay mr merge <n>" {
		t.Errorf("bare usage = %q", got)
	}
}

func TestDiffPaint(t *testing.T) {
	patch := " go.mod | 2 +-\n 1 file changed\n\ndiff --git a/go.mod b/go.mod\nindex a..b 100644\n--- a/go.mod\n+++ b/go.mod\n@@ -1,2 +1,2 @@ require (\n same\n-old\n+new\n"
	if got := (Term{}).diff(patch); got != patch {
		t.Errorf("plain changed:\n%s", got)
	}
	if got := (Term{Cols: 80}).diff(patch); got != patch {
		t.Errorf("no colour changed:\n%s", got)
	}
	got := Term{Cols: 80, Color: true}.diff(patch)
	for _, want := range []string{
		" go.mod | 2 " + sgrGreen + "+" + sgrReset + sgrRed + "-" + sgrReset,
		sgrBold + "diff --git a/go.mod b/go.mod" + sgrReset,
		sgrBold + "--- a/go.mod" + sgrReset,
		sgrBold + "+++ b/go.mod" + sgrReset,
		sgrCyan + "@@ -1,2 +1,2 @@" + sgrReset + " require (",
		"\n same\n",
		sgrRed + "-old" + sgrReset,
		sgrGreen + "+new" + sgrReset,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in\n%q", want, got)
		}
	}
	if stripSGR(got) != patch {
		t.Errorf("colour changed the text:\n%s", stripSGR(got))
	}
}

func TestDiffPaintRangeDiff(t *testing.T) {
	rd := "1:  abc1234 ! 1:  def5678 subject\n    @@ f.go\n    -old\n    +new\n"
	got := Term{Cols: 80, Color: true}.diff(rd)
	for _, want := range []string{
		sgrBold + "1:  abc1234 ! 1:  def5678 subject" + sgrReset,
		"    " + sgrRed + "-old" + sgrReset,
		"    " + sgrGreen + "+new" + sgrReset,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in\n%q", want, got)
		}
	}
}

func TestDiffIsSafe(t *testing.T) {
	got := Term{Cols: 80}.diff("+\x1b]52;c;aGk=\x07\n")
	if strings.ContainsRune(got, 0x1b) {
		t.Errorf("escape reached the terminal: %q", got)
	}
}

func TestBuildLog(t *testing.T) {
	log := "$ git clone x\n\x1b[32mok\x1b[0m\n$ make test\nFAIL\n"
	got := Term{Cols: 80, Color: true}.buildLog(log, "make test")
	want := sgrBold + "$ git clone x" + sgrReset + "\nok\n" + sgrBold + sgrRed + "$ make test" + sgrReset + "\nFAIL\n"
	if got != want {
		t.Errorf("buildLog:\n%q\nwant\n%q", got, want)
	}
	if got := (Term{Cols: 80}).buildLog("a\x1b]52;c;aGk=\x07b\n", ""); strings.ContainsRune(got, 0x1b) {
		t.Errorf("escape reached the terminal: %q", got)
	}
}

func TestLinkAndSwatch(t *testing.T) {
	on := Term{Cols: 80, Color: true, TrueColor: true, Links: true}
	if got := on.link("https://x.test/a", "a"); got != "\x1b]8;;https://x.test/a\x1b\\a\x1b]8;;\x1b\\" {
		t.Errorf("link = %q", got)
	}
	if got := on.link("https://x.test/\x1b", "a"); got != "a" {
		t.Errorf("control byte in url = %q", got)
	}
	if got := (Term{Cols: 80, Color: true}).link("https://x.test/a", "a"); got != "a" {
		t.Errorf("without links = %q", got)
	}
	if got := on.swatch("● #cf222e"); got != "\x1b[38;2;207;34;46m●"+sgrReset+" #cf222e" {
		t.Errorf("swatch = %q", got)
	}
	if got := on.swatch("● #nothex"); got != "● #nothex" {
		t.Errorf("bad hex = %q", got)
	}
}

func TestGlyph(t *testing.T) {
	for _, c := range []struct{ state, g, sgr string }{
		{"success", "✓", sgrGreen},
		{"approved", "✓", sgrGreen},
		{"merged", "✓", ""},
		{"failure", "✗", sgrRed},
		{"changes requested", "✗", sgrRed},
		{"signed_key_revoked", "✗", sgrRed},
		{"running", "◐", ""},
		{"pending", "◐", ""},
		{"closed", "○", ""},
		{"draft", "○", ""},
		{"open", "", ""},
		{"", "", ""},
	} {
		g, sgr := glyph(c.state)
		if g != c.g || sgr != c.sgr {
			t.Errorf("glyph(%q) = %q %q, want %q %q", c.state, g, sgr, c.g, c.sgr)
		}
	}
}
