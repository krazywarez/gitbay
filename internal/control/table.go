package control

import (
	"io"
	"slices"
	"strconv"
	"strings"
)

type cellKind int

const (
	kindText cellKind = iota
	kindFlex
	kindRef
	kindState
	kindAge
	kindNum
	kindSize
	kindSwatch
	kindGlyph
	kindMeta
)

// cell is one column of a table row. The kind decides colour, time
// format, and whether the column may be clipped to fit the terminal.
type cell struct {
	kind cellKind
	s    string
	sgr  string // colour for a kindText cell whose meaning is not its word
	url  string // the page a kindRef cell links to, when the terminal shows links
}

func cRef(s string) cell   { return cell{kind: kindRef, s: s} }
func cState(s string) cell { return cell{kind: kindState, s: s} }
func cText(s string) cell  { return cell{kind: kindText, s: s} }
func cFlex(s string) cell  { return cell{kind: kindFlex, s: s} }
func cAge(ts string) cell  { return cell{kind: kindAge, s: ts} }
func cNum(n int64) cell    { return cell{kind: kindNum, s: strconv.FormatInt(n, 10)} }
func cSize(n int64) cell   { return cell{kind: kindSize, s: strconv.FormatInt(n, 10)} }

// cLink is a reference that links to its page at a terminal that shows
// links; url is a path on this instance or an absolute URL.
func cLink(s, url string) cell { return cell{kind: kindRef, s: s, url: url} }

// cSwatch is a label colour: a coloured dot before the hex at a terminal
// with 24-bit colour.
func cSwatch(hex string) cell { return cell{kind: kindSwatch, s: hex} }

// cMark is text coloured for what it says about the row rather than for
// its word: "2 failed" red, "review requested" yellow.
func cMark(s, sgr string) cell { return cell{kind: kindText, s: s, sgr: sgr} }

// cGlyph is a state's mark, coloured for the state.
func cGlyph(state string) cell {
	g, sgr := glyph(state)
	return cell{kind: kindGlyph, s: g, sgr: sgr}
}

// cYou is the mark for a row that waits on the viewer.
func cYou() cell { return cell{kind: kindGlyph, s: "●", sgr: sgrYellow} }

// cMeta is a row's trailing facts, dim and joined by " · ". Empty parts
// are skipped.
func cMeta(parts ...string) cell {
	var keep []string
	for _, p := range parts {
		if p != "" {
			keep = append(keep, p)
		}
	}
	return cell{kind: kindMeta, s: strings.Join(keep, " · ")}
}

// table is a list command's rows. Plain, each row is written as it
// comes, tab-separated with no header. At a terminal rows are held
// until flush, then padded and fitted to the width, under a header only
// when a column holds numbers.
type table struct {
	term   Term
	w      io.Writer
	header []string
	rows   [][]cell
}

// note adds a marker to a row: its own trailing cell in plain output,
// as rows have always carried it, and joined to the state cell at
// cells[at] at a terminal ("private, archived"), so the table has no
// unnamed column.
func (c *Ctx) note(cells []cell, at int, plain, word string) []cell {
	if c.Term.Cols == 0 {
		return append(cells, cText(plain))
	}
	cells[at].s += ", " + word
	return cells
}

func (c *Ctx) table(w io.Writer, header ...string) *table {
	return &table{term: c.Term, w: w, header: header}
}

func (t *table) row(cs ...cell) {
	if t.term.Cols == 0 {
		parts := make([]string, len(cs))
		for i, c := range cs {
			if c.kind == kindAge {
				parts[i] = stamp(c.s)
			} else {
				parts[i] = c.s
			}
		}
		io.WriteString(t.w, strings.Join(parts, "\t")+"\n")
		return
	}
	now := termNow()
	for i := range cs {
		cs[i].s = termSafe(cs[i].s)
		switch cs[i].kind {
		case kindAge:
			cs[i].s = relAge(cs[i].s, now)
		case kindSize:
			if n, err := strconv.ParseInt(cs[i].s, 10, 64); err == nil {
				cs[i].s = humanBytes(n)
			}
		case kindSwatch:
			if t.term.TrueColor && rgb(cs[i].s) != "" {
				cs[i].s = "● " + cs[i].s
			}
		}
	}
	t.rows = append(t.rows, cs)
}

func (t *table) flush() {
	if t.term.Cols == 0 || len(t.rows) == 0 {
		return
	}
	var b strings.Builder
	for _, l := range t.lines() {
		b.WriteString(l + "\n")
	}
	io.WriteString(t.w, b.String())
}

// numeric reports whether a column holds numbers or sizes, which need a
// header to say what they count.
func (t *table) numeric() bool {
	for _, r := range t.rows {
		for _, c := range r {
			if c.kind == kindNum || c.kind == kindSize {
				return true
			}
		}
	}
	return false
}

// lines lays the rows out at the terminal width: a dim header first
// only when a column is a number, then each row padded and painted.
func (t *table) lines() []string {
	if !t.numeric() {
		t.header = nil
	}
	t.dropEmpty()
	// The column count is never smaller than the longest row: a row with
	// more cells than the header has still gets every cell rendered, the
	// header just shows blank above the ones it doesn't name.
	n := len(t.header)
	for _, r := range t.rows {
		n = max(n, len(r))
	}
	widths := make([]int, n)
	for i, h := range t.header {
		widths[i] = cells(h)
	}
	for _, r := range t.rows {
		for i := 0; i < len(r); i++ {
			widths[i] = max(widths[i], cells(r[i].s))
		}
	}
	t.capSparse(widths)
	t.fit(widths)

	var out []string
	line := make([]string, n)
	if len(t.header) > 0 {
		for i := range line {
			line[i] = ""
			if i < len(t.header) {
				line[i] = clip(t.header[i], widths[i])
			}
		}
		out = append(out, t.term.paint(sgrDim, strings.TrimRight(t.join(line, widths), " ")))
	}
	for _, r := range t.rows {
		for i := 0; i < n; i++ {
			s := ""
			if i < len(r) {
				s = clip(r[i].s, widths[i])
			}
			line[i] = s
		}
		out = append(out, strings.TrimRight(t.joinRow(r, line, widths), " "))
	}
	return out
}

// dropEmpty removes a column that is blank on every row, header and
// all: an issue list where nothing is labelled has no LABELS column.
func (t *table) dropEmpty() {
	n := len(t.header)
	for _, r := range t.rows {
		n = max(n, len(r))
	}
	for i := n - 1; i >= 0; i-- {
		empty := true
		for _, r := range t.rows {
			if i < len(r) && r[i].s != "" {
				empty = false
				break
			}
		}
		if !empty {
			continue
		}
		if i < len(t.header) {
			t.header = slices.Delete(slices.Clone(t.header), i, i+1)
		}
		for j, r := range t.rows {
			if i < len(r) {
				t.rows[j] = slices.Delete(r, i, i+1)
			}
		}
	}
}

// capSparse narrows a flexible column that is blank on most rows to a
// third of the terminal, so a few long values do not push every other
// row's later columns to the right edge.
func (t *table) capSparse(widths []int) {
	for i := range widths {
		filled, flex := 0, false
		for _, r := range t.rows {
			if i < len(r) && r[i].kind == kindFlex {
				flex = true
				if r[i].s != "" {
					filled++
				}
			}
		}
		if flex && filled*2 < len(t.rows) {
			widths[i] = min(widths[i], max(8, t.term.Cols/3))
		}
	}
}

// fit shrinks columns until a row fits the terminal: the flexible
// column first, down to 8 cells, then the other text columns from the
// right, down to 8 each.
func (t *table) fit(widths []int) {
	total := func() int {
		s := 2 * (len(widths) - 1)
		for _, w := range widths {
			s += w
		}
		return s
	}
	kinds := make([]cellKind, len(widths))
	for i := range widths {
		for _, r := range t.rows {
			if i < len(r) {
				kinds[i] = r[i].kind
				break
			}
		}
	}
	shrink := func(i int) {
		if over := total() - t.term.Cols; over > 0 && widths[i] > 8 {
			widths[i] = max(8, widths[i]-over)
		}
	}
	for i, k := range kinds {
		if k == kindFlex {
			shrink(i)
		}
	}
	for i := len(kinds) - 1; i >= 0; i-- {
		if kinds[i] == kindText || kinds[i] == kindMeta {
			shrink(i)
		}
	}
}

// join pads every column but the last and separates them by two spaces.
func (t *table) join(line []string, widths []int) string {
	var b strings.Builder
	for i, s := range line {
		if i > 0 {
			b.WriteString("  ")
		}
		if i == len(line)-1 {
			b.WriteString(s)
		} else {
			b.WriteString(pad(s, widths[i]))
		}
	}
	return b.String()
}

// joinRow is join with state cells coloured after padding, so the
// SGR bytes never count against the width.
func (t *table) joinRow(r []cell, line []string, widths []int) string {
	var b strings.Builder
	for i, s := range line {
		if i > 0 {
			b.WriteString("  ")
		}
		padding := ""
		if i < len(line)-1 {
			padding = strings.Repeat(" ", max(0, widths[i]-cells(s)))
		}
		if i < len(r) {
			switch r[i].kind {
			case kindState:
				s = t.term.paintState(s)
			case kindRef:
				s = t.term.link(r[i].url, t.term.paint(sgrDim, s))
			case kindGlyph:
				s = t.term.paint(r[i].sgr, s)
			case kindMeta:
				s = t.term.paint(sgrDim, s)
			case kindSwatch:
				s = t.term.swatch(s)
			default:
				s = t.term.paint(r[i].sgr, s)
			}
		}
		b.WriteString(s + padding)
	}
	return b.String()
}

// stripSGR removes SGR sequences, for tests and width checks.
func stripSGR(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == 0x1b {
			if j := strings.IndexByte(s[i:], 'm'); j >= 0 {
				i += j
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}
