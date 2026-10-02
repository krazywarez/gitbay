package control

import (
	"fmt"
	"io"
	"strconv"
	"strings"

	"gitbay.org/gitbay/internal/termtext"
)

// screen is a show or a list as a terminal draws it: a header block of
// labelled fields, a body, counted sections, and the commands that
// apply. Commands build one only at a terminal; plain and --json output
// never reach it.
type screen struct {
	fields   []field
	body     string // markup source
	format   string // the body's markup format; "text" is not rendered
	sections []section
	actions  []action
}

// field is one header line: a label and the cells after it.
type field struct {
	label string
	value []cell
}

// section is a titled run of rows. n is the total, shown as "(n)"; when
// it is more than the rows shown, more is the command for the rest. An
// empty section is left out unless empty says to draw "(0)".
type section struct {
	title string
	n     int
	rows  []row
	more  []string
	empty bool
}

// row is a section row, and optionally the markup that follows it (a
// comment's body).
type row struct {
	cells  []cell
	body   string
	format string
}

func rowOf(cs ...cell) row { return row{cells: cs} }

// action is one command in the legend, under its group's name. argv is
// the command as typed after "gitbay".
type action struct {
	group string
	argv  []string
}

// render draws s: the parts in order, one blank line between parts and
// between sections.
func (c *Ctx) render(w io.Writer, s screen) {
	var blocks []string
	if b := c.renderFields(s.fields); b != "" {
		blocks = append(blocks, b)
	}
	if b := c.renderBody(s.body, s.format, ""); b != "" {
		blocks = append(blocks, b)
	}
	for _, sec := range s.sections {
		if b := c.renderSection(sec); b != "" {
			blocks = append(blocks, b)
		}
	}
	if b := c.renderLegend(s.actions); b != "" {
		blocks = append(blocks, b)
	}
	io.WriteString(w, strings.Join(blocks, "\n"))
}

// renderFields aligns labels on the widest, dim, with a colon. Every
// cell but the last is painted as a table paints it; the last wraps to
// the width, continuation lines under its first column.
func (c *Ctx) renderFields(fs []field) string {
	t := c.Term
	wide := 0
	for _, f := range fs {
		if len(f.value) > 0 {
			wide = max(wide, cells(f.label)+1)
		}
	}
	var b strings.Builder
	for _, f := range fs {
		if len(f.value) == 0 {
			continue
		}
		prefix := t.paint(sgrDim, f.label+":") + strings.Repeat(" ", wide-cells(f.label)-1+2)
		col := wide + 2
		head := ""
		for _, cl := range f.value[:len(f.value)-1] {
			s := c.cellText(cl)
			if s == "" {
				continue
			}
			head += c.paintCell(cl, s) + "  "
			col += cells(s) + 2
		}
		last := f.value[len(f.value)-1]
		indent := strings.Repeat(" ", col)
		for i, l := range termtext.Wrap(c.cellText(last), max(8, t.Cols-col)) {
			p := indent
			if i == 0 {
				p = prefix + head
			}
			b.WriteString(strings.TrimRight(p+c.paintCell(last, l), " ") + "\n")
		}
	}
	return b.String()
}

// cellText is a cell's terminal text: made safe, ages relative, sizes
// humanized, as table.row prepares it.
func (c *Ctx) cellText(cl cell) string {
	s := termSafe(cl.s)
	switch cl.kind {
	case kindAge:
		return relAge(s, termNow())
	case kindSize:
		if n, err := strconv.ParseInt(s, 10, 64); err == nil {
			return humanBytes(n)
		}
	}
	return s
}

// paintCell paints text s as its cell's kind paints at a terminal.
func (c *Ctx) paintCell(cl cell, s string) string {
	t := c.Term
	switch cl.kind {
	case kindState:
		return t.paintState(s)
	case kindRef:
		return t.link(cl.url, t.paint(sgrDim, s))
	case kindMeta:
		return t.paint(sgrDim, s)
	case kindSwatch:
		return t.swatch(s)
	}
	return t.paint(cl.sgr, s)
}

// renderBody renders markup to the width, each line prefixed by indent.
// Format "text" is wrapped as it is, without markup.
func (c *Ctx) renderBody(src, format, indent string) string {
	if strings.TrimSpace(src) == "" {
		return ""
	}
	width := max(0, c.Term.Cols-len(indent))
	var out []string
	if format == "text" {
		for _, para := range strings.Split(strings.TrimRight(termSafe(src), "\n"), "\n") {
			out = append(out, termtext.Wrap(para, width)...)
		}
	} else {
		opts := termtext.Options{Width: width, Color: c.Term.Color, Base: c.Cfg.Server.SiteURL}
		out = strings.Split(strings.TrimRight(termtext.Render(termSafe(src), format, opts), "\n"), "\n")
	}
	var b strings.Builder
	for _, l := range out {
		if l == "" {
			b.WriteString("\n")
			continue
		}
		b.WriteString(indent + l + "\n")
	}
	return b.String()
}

// renderSection is the heading, the rows laid out as one table with no
// header, each row's body indented beneath it, and the "+n more" line.
func (c *Ctx) renderSection(s section) string {
	if len(s.rows) == 0 && !s.empty {
		return ""
	}
	var b strings.Builder
	b.WriteString(c.Term.paint(sgrBold+sgrBlue, fmt.Sprintf("%s (%d)", s.title, s.n)) + "\n")
	if len(s.rows) > 0 {
		tb := &table{term: c.Term, w: io.Discard}
		for _, r := range s.rows {
			tb.row(append([]cell(nil), r.cells...)...)
		}
		for i, l := range tb.lines() {
			b.WriteString(l + "\n")
			b.WriteString(c.renderBody(s.rows[i].body, s.rows[i].format, "  "))
		}
	}
	if rest := s.n - len(s.rows); rest > 0 && len(s.more) > 0 {
		b.WriteString(c.Term.paint(sgrDim, fmt.Sprintf("+%d more  %s", rest, c.cmdline(s.more))) + "\n")
	}
	return b.String()
}

// cmdline is argv as the viewer would type it: "gitbay", the words
// shell-quoted, and the repository the CLI inferred left out.
func (c *Ctx) cmdline(argv []string) string {
	words := []string{"gitbay"}
	for _, a := range argv {
		if c.Term.Here != "" && a == c.Term.Here {
			continue
		}
		words = append(words, shellWord(a))
	}
	return strings.Join(words, " ")
}

// renderLegend is a rule, then the action groups in the order they first
// appear, each a bold name over its commands in blue. Groups sit side by
// side, three to a band, when the terminal is 80 wide or more and the
// band fits; otherwise they stack.
func (c *Ctx) renderLegend(as []action) string {
	if len(as) == 0 {
		return ""
	}
	t := c.Term
	type group struct {
		name string
		cmds []string
	}
	var groups []*group
	byName := map[string]*group{}
	for _, a := range as {
		g := byName[a.group]
		if g == nil {
			g = &group{name: a.group}
			byName[a.group] = g
			groups = append(groups, g)
		}
		g.cmds = append(g.cmds, c.cmdline(a.argv))
	}
	var b strings.Builder
	b.WriteString(t.paint(sgrDim, strings.Repeat("─", t.Cols)) + "\n")
	for start := 0; start < len(groups); start += 3 {
		band := groups[start:min(start+3, len(groups))]
		widths := make([]int, len(band))
		total, height := 2*(len(band)-1), 0
		for i, g := range band {
			widths[i] = cells(g.name)
			for _, cmd := range g.cmds {
				widths[i] = max(widths[i], cells(cmd))
			}
			total += widths[i]
			height = max(height, len(g.cmds))
		}
		if t.Cols < 80 || total > t.Cols {
			for _, g := range band {
				b.WriteString(t.paint(sgrBold, g.name) + "\n")
				for _, cmd := range g.cmds {
					b.WriteString(t.paint(sgrBlue, cmd) + "\n")
				}
			}
			continue
		}
		for line := -1; line < height; line++ {
			var l strings.Builder
			for i, g := range band {
				s, sgr := "", sgrBlue
				if line < 0 {
					s, sgr = g.name, sgrBold
				} else if line < len(g.cmds) {
					s = g.cmds[line]
				}
				l.WriteString(t.paint(sgr, s))
				if i < len(band)-1 {
					l.WriteString(strings.Repeat(" ", widths[i]-cells(s)+2))
				}
			}
			b.WriteString(strings.TrimRight(l.String(), " ") + "\n")
		}
	}
	return b.String()
}
