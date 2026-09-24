package control

import (
	"io"
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
)

// cell is one column of a table row. The kind decides colour, time
// format, and whether the column may be clipped to fit the terminal.
type cell struct {
	kind cellKind
	s    string
}

func cRef(s string) cell   { return cell{kindRef, s} }
func cState(s string) cell { return cell{kindState, s} }
func cText(s string) cell  { return cell{kindText, s} }
func cFlex(s string) cell  { return cell{kindFlex, s} }
func cAge(ts string) cell  { return cell{kindAge, ts} }
func cNum(n int64) cell    { return cell{kindNum, strconv.FormatInt(n, 10)} }

// table is a list command's rows. Plain, each row is written as it
// comes, tab-separated with no header. At a terminal rows are held
// until flush, then written under a header, padded, and fitted to the
// width.
type table struct {
	term   Term
	w      io.Writer
	header []string
	rows   [][]cell
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
		if cs[i].kind == kindAge {
			cs[i].s = relAge(cs[i].s, now)
		}
	}
	t.rows = append(t.rows, cs)
}

func (t *table) flush() {
	if t.term.Cols == 0 || len(t.rows) == 0 {
		return
	}
	n := len(t.header)
	widths := make([]int, n)
	for i, h := range t.header {
		widths[i] = cells(h)
	}
	for _, r := range t.rows {
		for i := 0; i < n && i < len(r); i++ {
			widths[i] = max(widths[i], cells(r[i].s))
		}
	}
	t.fit(widths)

	var b strings.Builder
	line := make([]string, n)
	for i, h := range t.header {
		line[i] = h
	}
	b.WriteString(t.term.paint(sgrDim, t.join(line, widths)) + "\n")
	for _, r := range t.rows {
		for i := 0; i < n; i++ {
			s := ""
			if i < len(r) {
				s = clip(r[i].s, widths[i])
			}
			line[i] = s
		}
		b.WriteString(t.joinRow(r, line, widths) + "\n")
	}
	io.WriteString(t.w, b.String())
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
	if len(t.rows) > 0 {
		for i := range widths {
			if i < len(t.rows[0]) {
				kinds[i] = t.rows[0][i].kind
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
		if kinds[i] == kindText {
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
		if i < len(r) && r[i].kind == kindState {
			s = t.term.paint(stateColor(s), s)
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
