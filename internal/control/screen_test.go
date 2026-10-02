package control

import (
	"bytes"
	"fmt"
	"io"
	"strings"
	"testing"
)

func screenCtx(cols int, color bool) *Ctx {
	return &Ctx{Term: Term{Cols: cols, Color: color}}
}

func renderString(c *Ctx, s screen) string {
	var b strings.Builder
	c.render(&b, s)
	return b.String()
}

func sampleScreen() screen {
	return screen{
		fields: []field{
			{"Merge", []cell{cRef("!552"), cText("wire $PAGER through long views")}},
			{"State", []cell{cState("open"), cMeta("cli-pager → main", "cmc")}},
			{"Checks", []cell{cGlyph("running"), cText("1 running")}},
		},
		body:   "Pages long views.",
		format: "md",
		sections: []section{
			{title: "Commits", n: 2, rows: []row{
				rowOf(cRef("8f3a1c2"), cFlex("cli: page long output")),
				rowOf(cRef("2b77e90"), cFlex("control: mark views")),
			}},
			{title: "Discussion", n: 0, empty: true},
			{title: "Hidden", n: 0},
		},
	}
}

func TestRenderPlainLayout(t *testing.T) {
	got := renderString(screenCtx(80, false), sampleScreen())
	want := `Merge:   !552  wire $PAGER through long views
State:   open  cli-pager → main · cmc
Checks:  ◐  1 running

Pages long views.

Commits (2)
8f3a1c2  cli: page long output
2b77e90  control: mark views

Discussion (0)
`
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestRenderColourIsOnlyPaint(t *testing.T) {
	for _, cols := range []int{80, 120} {
		plain := renderString(screenCtx(cols, false), sampleScreen())
		colour := renderString(screenCtx(cols, true), sampleScreen())
		if stripSGR(colour) != plain {
			t.Errorf("cols %d: colour render differs beyond SGR:\n%s\n---\n%s", cols, stripSGR(colour), plain)
		}
		if !strings.Contains(colour, sgrBold+sgrBlue+"Commits (2)"+sgrReset) {
			t.Errorf("cols %d: heading not bold blue: %q", cols, colour)
		}
		if !strings.Contains(colour, sgrDim+"Merge:"+sgrReset) {
			t.Errorf("cols %d: label not dim: %q", cols, colour)
		}
	}
}

func TestRenderMoreLine(t *testing.T) {
	s := screen{sections: []section{{title: "Builds", n: 14, more: []string{"build", "list", "krz/gitbay"},
		rows: []row{rowOf(cRef("1779"), cFlex("test"))}}}}
	got := renderString(screenCtx(80, false), s)
	if !strings.HasSuffix(got, "1779  test\n+13 more  gitbay build list krz/gitbay\n") {
		t.Errorf("got %q", got)
	}
}

func TestRenderRowBody(t *testing.T) {
	s := screen{sections: []section{{title: "Discussion", n: 1, rows: []row{
		{cells: []cell{cText("cmc"), cMeta("2h")}, body: "Looks good.", format: "md"},
	}}}}
	got := renderString(screenCtx(80, false), s)
	want := "Discussion (1)\ncmc  2h\n  Looks good.\n"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestRenderTextBodyIsNotMarkup(t *testing.T) {
	s := screen{body: "a *b* c", format: "text"}
	if got := renderString(screenCtx(80, false), s); got != "a *b* c\n" {
		t.Errorf("got %q", got)
	}
}

func TestRenderFieldWraps(t *testing.T) {
	long := strings.Repeat("word ", 20)
	s := screen{fields: []field{{"Merge", []cell{cRef("!1"), cText(strings.TrimSpace(long))}}}}
	out := renderString(screenCtx(40, false), s)
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) < 2 {
		t.Fatalf("did not wrap: %q", out)
	}
	for _, l := range lines {
		if cells(l) > 40 {
			t.Errorf("line wider than 40: %q", l)
		}
	}
	if !strings.HasPrefix(lines[1], strings.Repeat(" ", len("Merge:  !1  "))) {
		t.Errorf("continuation not under the value: %q", lines[1])
	}
}

func TestCmdlineDropsHere(t *testing.T) {
	c := screenCtx(80, false)
	c.Term.Here = "krz/gitbay"
	for _, tc := range []struct {
		argv []string
		want string
	}{
		{[]string{"mr", "diff", "krz/gitbay", "552"}, "gitbay mr diff 552"},
		{[]string{"issue", "show", "krz/hutch", "3"}, "gitbay issue show krz/hutch 3"},
		{[]string{"issue", "list", "--label", "needs review"}, "gitbay issue list --label 'needs review'"},
	} {
		if got := c.cmdline(tc.argv); got != tc.want {
			t.Errorf("cmdline(%q) = %q, want %q", tc.argv, got, tc.want)
		}
	}
}

func legendSample() []action {
	return []action{
		{"Unblock", []string{"mr", "rebase", "552"}},
		{"Review", []string{"mr", "review", "krz/gitbay", "552", "--approve"}},
		{"Review", []string{"mr", "comment", "krz/gitbay", "552"}},
		{"Read", []string{"mr", "diff", "krz/gitbay", "552"}},
	}
}

func TestLegendColumns(t *testing.T) {
	c := screenCtx(120, false)
	c.Term.Here = "krz/gitbay"
	got := c.renderLegend(legendSample())
	want := strings.Repeat("─", 120) + "\n" +
		fmt.Sprintf("%-22s%-32s%s\n", "Unblock", "Review", "Read") +
		fmt.Sprintf("%-22s%-32s%s\n", "gitbay mr rebase 552", "gitbay mr review 552 --approve", "gitbay mr diff 552") +
		fmt.Sprintf("%-22s%s\n", "", "gitbay mr comment 552")
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestLegendStacksWhenNarrow(t *testing.T) {
	c := screenCtx(60, false)
	c.Term.Here = "krz/gitbay"
	got := c.renderLegend(legendSample())
	want := strings.Repeat("─", 60) + "\n" +
		"Unblock\ngitbay mr rebase 552\n" +
		"Review\ngitbay mr review 552 --approve\ngitbay mr comment 552\n" +
		"Read\ngitbay mr diff 552\n"
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestLegendColour(t *testing.T) {
	got := screenCtx(120, true).renderLegend(legendSample())
	if !strings.Contains(got, sgrBlue+"gitbay mr rebase 552"+sgrReset) || !strings.Contains(got, sgrBold+"Unblock"+sgrReset) {
		t.Errorf("legend paint: %q", got)
	}
	if stripSGR(got) != screenCtx(120, false).renderLegend(legendSample()) {
		t.Error("colour legend differs beyond SGR")
	}
}

func TestEmitViewRoutes(t *testing.T) {
	built := false
	build := func() screen { built = true; return screen{fields: []field{{"Repo", []cell{cText("a/b")}}}} }
	plain := func(w io.Writer) { io.WriteString(w, "plain\n") }

	var out bytes.Buffer
	c := &Ctx{Stdout: &out, Stderr: io.Discard}
	c.emitView(map[string]string{"k": "v"}, plain, build)
	if out.String() != "plain\n" || built {
		t.Errorf("piped: %q built=%v", out.String(), built)
	}

	out.Reset()
	c = &Ctx{Stdout: &out, Stderr: io.Discard, JSON: true, Term: Term{Cols: 80}}
	c.emitView(map[string]string{"k": "v"}, plain, build)
	if !strings.Contains(out.String(), `"k":"v"`) || built {
		t.Errorf("json: %q built=%v", out.String(), built)
	}

	out.Reset()
	c = &Ctx{Stdout: &out, Stderr: io.Discard, Term: Term{Cols: 80}}
	c.emitView(map[string]string{"k": "v"}, plain, build)
	if out.String() != "Repo:  a/b\n" || !built {
		t.Errorf("terminal: %q built=%v", out.String(), built)
	}
}

// errorer is the part of *testing.T checkActions uses, so its own test
// can pass a recorder.
type errorer interface {
	Helper()
	Errorf(format string, args ...any)
}

type recorder struct{ failed bool }

func (r *recorder) Helper()               {}
func (r *recorder) Errorf(string, ...any) { r.failed = true }

// cliLocal are commands cmd/gitbay runs itself; the registry does not
// know them, but a legend may suggest them.
var cliLocal = map[string]bool{"mr rebase": true, "mr checkout": true, "repo clone": true}

// checkActions fails t for any legend or "more" command that the
// registry would not dispatch, or whose flags it does not declare.
func checkActions(t errorer, s screen) {
	t.Helper()
	var all [][]string
	for _, a := range s.actions {
		all = append(all, a.argv)
	}
	for _, sec := range s.sections {
		if len(sec.more) > 0 {
			all = append(all, sec.more)
		}
	}
	for _, argv := range all {
		if len(argv) >= 2 && cliLocal[argv[0]+" "+argv[1]] {
			continue
		}
		cmd, rest, ok := Lookup(argv)
		if !ok {
			t.Errorf("no command for %q", argv)
			continue
		}
		if err := checkFlags(cmd, rest); err != nil {
			t.Errorf("%q: %v", argv, err)
		}
	}
}

// checkFlags refuses a flag the command does not declare, and a value
// flag with nothing after it. Words that are not flags are positionals.
func checkFlags(cmd Command, rest []string) error {
	takes := map[string]bool{}
	for _, f := range cmd.Flags {
		takes[f.Name] = f.Arg != ""
	}
	for i := 0; i < len(rest); i++ {
		a := rest[i]
		if !strings.HasPrefix(a, "--") {
			continue
		}
		name, _, inline := strings.Cut(a, "=")
		value, ok := takes[name]
		if !ok {
			return fmt.Errorf("%s does not take %s", joinPath(cmd.Path), name)
		}
		if value && !inline {
			if i+1 >= len(rest) {
				return fmt.Errorf("%s needs a value", name)
			}
			i++
		}
	}
	return nil
}

func TestCheckActions(t *testing.T) {
	for _, tc := range []struct {
		argv []string
		fail bool
	}{
		{[]string{"mr", "dif", "a/b", "1"}, true},
		{[]string{"mr", "diff", "a/b", "1", "--bogus"}, true},
		{[]string{"mr", "review", "a/b", "1", "--approve"}, false},
		{[]string{"mr", "list", "a/b", "--state"}, true},
		{[]string{"mr", "list", "a/b", "--state", "all"}, false},
		{[]string{"mr", "rebase", "1"}, false},
	} {
		r := &recorder{}
		checkActions(r, screen{actions: []action{{"G", tc.argv}}})
		if r.failed != tc.fail {
			t.Errorf("%q: failed = %v, want %v", tc.argv, r.failed, tc.fail)
		}
	}
}
