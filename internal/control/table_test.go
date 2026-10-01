package control

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func fixtureTable(c *Ctx, w *bytes.Buffer) {
	tb := c.table(w, "#", "STATE", "TITLE", "AUTHOR")
	tb.row(cRef("#252"), cState("open"), cFlex("Dependency updates available for every module"), cText("gitbay-bot"))
	tb.row(cRef("#12"), cState("closed"), cFlex("Android app"), cText("cmc"))
	tb.flush()
}

func TestTablePlainIsTabs(t *testing.T) {
	var b bytes.Buffer
	fixtureTable(&Ctx{}, &b)
	want := "#252\topen\tDependency updates available for every module\tgitbay-bot\n" +
		"#12\tclosed\tAndroid app\tcmc\n"
	if b.String() != want {
		t.Errorf("plain:\n%q\nwant\n%q", b.String(), want)
	}
}

func TestTableTerminalFits(t *testing.T) {
	var b bytes.Buffer
	fixtureTable(&Ctx{Term: Term{Cols: 40}}, &b)
	want := "#     STATE   TITLE           AUTHOR\n" +
		"#252  open    Dependency up…  gitbay-bot\n" +
		"#12   closed  Android app     cmc\n"
	if b.String() != want {
		t.Errorf("terminal:\n%s\nwant\n%s", b.String(), want)
	}
}

// A column shrunk below its header's width clips the header too.
func TestTableClipsHeaderToColumn(t *testing.T) {
	var b bytes.Buffer
	tb := (&Ctx{Term: Term{Cols: 16}}).table(&b, "FINGERPRINT", "SCOPE")
	tb.row(cFlex("SHA256:abcdefghijklmnopqrstuvwxyz"), cState("full"))
	tb.flush()
	for _, line := range strings.Split(strings.TrimSuffix(b.String(), "\n"), "\n") {
		if cells(line) > 16 {
			t.Errorf("line of %d cells at 16 columns: %q", cells(line), line)
		}
	}
	if !strings.HasPrefix(b.String(), "FINGERPR…  SCOPE\n") {
		t.Errorf("header:\n%s", b.String())
	}
}

func TestTableColourOnlyAddsSGR(t *testing.T) {
	var mono, colour bytes.Buffer
	fixtureTable(&Ctx{Term: Term{Cols: 40}}, &mono)
	fixtureTable(&Ctx{Term: Term{Cols: 40, Color: true}}, &colour)
	if !strings.Contains(colour.String(), sgrGreen+"open"+sgrReset) {
		t.Errorf("open not green: %q", colour.String())
	}
	if !strings.HasPrefix(colour.String(), sgrDim) {
		t.Errorf("header not dim: %q", colour.String())
	}
	if stripSGR(colour.String()) != mono.String() {
		t.Errorf("colour changed the layout:\n%s\nvs\n%s", stripSGR(colour.String()), mono.String())
	}
}

func TestTableAgesAndPlainStamps(t *testing.T) {
	termNow = func() time.Time { return time.Date(2026, 9, 23, 12, 0, 1, 0, time.UTC) }
	t.Cleanup(func() { termNow = time.Now })
	var plain, term bytes.Buffer
	for _, c := range []struct {
		ctx *Ctx
		w   *bytes.Buffer
	}{{&Ctx{}, &plain}, {&Ctx{Term: Term{Cols: 80}}, &term}} {
		tb := c.ctx.table(c.w, "#", "UPDATED")
		tb.row(cRef("#1"), cAge("2026-09-23T10:00:00.123Z"))
		tb.flush()
	}
	if plain.String() != "#1\t2026-09-23T10:00:00Z\n" {
		t.Errorf("plain = %q", plain.String())
	}
	if term.String() != "#   UPDATED\n#1  2h ago\n" {
		t.Errorf("term = %q", term.String())
	}
}

// A row may carry a cell beyond what the header names — repo list's
// trailing [archived] marker, only present on some rows. flush must
// still render it, not silently drop it because it falls past
// len(header).
func TestTableKeepsCellsBeyondTheHeader(t *testing.T) {
	var b bytes.Buffer
	tb := (&Ctx{Term: Term{Cols: 80}}).table(&b, "PATH", "VISIBILITY", "DESCRIPTION")
	tb.row(cRef("a/x"), cState("public"), cFlex("one"))
	tb.row(cRef("a/y"), cState("public"), cFlex("two"), cText("[archived]"))
	tb.flush()

	out := b.String()
	if !strings.Contains(out, "[archived]") {
		t.Fatalf("archived marker dropped:\n%s", out)
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("want 3 lines, got %d:\n%s", len(lines), out)
	}
	header, row1, row2 := lines[0], lines[1], lines[2]
	descAt := strings.Index(header, "DESCRIPTION")
	oneAt := strings.Index(row1, "one")
	twoAt := strings.Index(row2, "two")
	if descAt < 0 || oneAt < 0 || twoAt < 0 {
		t.Fatalf("columns not found:\n%s", out)
	}
	if descAt != oneAt || descAt != twoAt {
		t.Errorf("DESCRIPTION column not aligned: header at %d, row1 at %d, row2 at %d\n%s", descAt, oneAt, twoAt, out)
	}
}

func TestTableEmptyPrintsNothing(t *testing.T) {
	var b bytes.Buffer
	(&Ctx{Term: Term{Cols: 80}}).table(&b, "#").flush()
	if b.Len() != 0 {
		t.Errorf("empty table printed %q", b.String())
	}
}

func TestTableNoTrailingSpace(t *testing.T) {
	var b bytes.Buffer
	tb := (&Ctx{Term: Term{Cols: 80}}).table(&b, "ADDRESS", "STATE")
	tb.row(cRef("a@example.com"), cState("verified"), cText("primary"))
	tb.row(cRef("b@example.com"), cState("verified"))
	tb.flush()
	for _, line := range strings.Split(b.String(), "\n") {
		if strings.HasSuffix(line, " ") {
			t.Errorf("trailing space: %q", line)
		}
	}
}

// A flexible column blank on most rows is capped at a third of the
// terminal.
func TestTableCapsSparseFlex(t *testing.T) {
	var b bytes.Buffer
	tb := (&Ctx{Term: Term{Cols: 90}}).table(&b, "TAG", "TITLE", "ASSETS")
	tb.row(cRef("v3"), cFlex(""), cText("2"))
	tb.row(cRef("v2"), cFlex(""), cText("2"))
	tb.row(cRef("v1"), cFlex(""), cText("2"))
	tb.row(cRef("v0"), cFlex(strings.Repeat("x", 60)), cText("2"))
	tb.flush()
	lines := strings.Split(b.String(), "\n")
	if at := strings.Index(lines[0], "ASSETS"); at != len("TAG  ")+30+2 {
		t.Errorf("ASSETS at %d:\n%s", at, b.String())
	}
}

func TestTableSizeCells(t *testing.T) {
	var plain, term bytes.Buffer
	for _, c := range []struct {
		ctx *Ctx
		w   *bytes.Buffer
	}{{&Ctx{}, &plain}, {&Ctx{Term: Term{Cols: 80}}, &term}} {
		tb := c.ctx.table(c.w, "NAME", "SIZE")
		tb.row(cRef("a"), cSize(2048))
		tb.flush()
	}
	if plain.String() != "a\t2048\n" {
		t.Errorf("plain = %q", plain.String())
	}
	if term.String() != "NAME  SIZE\na     2.0 KiB\n" {
		t.Errorf("term = %q", term.String())
	}
}
