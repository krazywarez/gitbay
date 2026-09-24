package control

import (
	"io"
	"strings"

	"gitbay.org/gitbay/internal/termtext"
)

// when is a stored timestamp in a view: the web's format at a
// terminal, RFC3339 to the second when plain.
func (c *Ctx) when(s string) string {
	if c.Term.Cols == 0 {
		return stamp(s)
	}
	t, ok := parseStamp(s)
	if !ok {
		return s
	}
	return t.Format("2006-01-02 15:04 UTC")
}

// siteURL is the instance's address with path segments appended.
func (c *Ctx) siteURL(parts ...string) string {
	return strings.TrimRight(c.Cfg.Server.SiteURL, "/") + "/" + strings.Join(parts, "/")
}

// view lays out a show: a title line, aligned fields, a body, events,
// comments. Plain output is the same lines without colour or wrapping.
type view struct {
	c *Ctx
	w io.Writer
}

func (c *Ctx) view(w io.Writer) *view { return &view{c: c, w: w} }

func (v *view) opts() termtext.Options {
	return termtext.Options{Width: max(0, v.c.Term.Cols-2), Color: v.c.Term.Color, Base: v.c.Cfg.Server.SiteURL}
}

// title prints "ref  title  state", wrapping title+state to the
// terminal width. Continuation lines indent under the title, and each
// line is painted after wrapping so no SGR sequence crosses a break.
// title or state may be "": either is skipped rather than leaving a
// trailing blank field.
func (v *view) title(ref, title, state string) {
	t := v.c.Term
	if t.Cols == 0 {
		switch {
		case title == "" && state == "":
			io.WriteString(v.w, ref+"\n")
		case state == "":
			io.WriteString(v.w, ref+"  "+t.paint(sgrBold, title)+"\n")
		case title == "":
			io.WriteString(v.w, ref+"  "+t.paint(stateColor(state), state)+"\n")
		default:
			io.WriteString(v.w, ref+"  "+t.paint(sgrBold, title)+"  "+t.paint(stateColor(state), state)+"\n")
		}
		return
	}
	prefix := ref + "  "
	indent := strings.Repeat(" ", cells(prefix))
	rest := title
	if state != "" {
		if rest != "" {
			rest += "  "
		}
		rest += state
	}
	if rest == "" {
		io.WriteString(v.w, ref+"\n")
		return
	}
	lines := termtext.Wrap(rest, t.Cols-cells(prefix))
	for i, line := range lines {
		p := indent
		if i == 0 {
			p = prefix
		}
		if i < len(lines)-1 || state == "" {
			io.WriteString(v.w, p+t.paint(sgrBold, line)+"\n")
			continue
		}
		// Last line carries the state word, the last word overall
		// (states are single words): keep it coloured, not bold.
		head, last := line, line
		if idx := strings.LastIndex(line, " "); idx >= 0 {
			head, last = line[:idx], line[idx+1:]
		} else {
			head = ""
		}
		io.WriteString(v.w, p)
		if head != "" {
			io.WriteString(v.w, t.paint(sgrBold, head)+" ")
		}
		io.WriteString(v.w, t.paint(stateColor(state), last)+"\n")
	}
}

// fields prints key/value pairs aligned on the widest key, skipping
// empty values. A value that does not fit wraps, its continuation
// lines indented to the value column.
func (v *view) fields(kv ...string) {
	wide := 0
	for i := 0; i+1 < len(kv); i += 2 {
		if kv[i+1] != "" {
			wide = max(wide, cells(kv[i]))
		}
	}
	io.WriteString(v.w, "\n")
	for i := 0; i+1 < len(kv); i += 2 {
		key, val := kv[i], kv[i+1]
		if val == "" {
			continue
		}
		prefix := "  " + v.c.Term.paint(sgrDim, pad(key, wide)) + "  "
		if v.c.Term.Cols == 0 {
			io.WriteString(v.w, prefix+val+"\n")
			continue
		}
		indent := strings.Repeat(" ", 2+wide+2)
		for j, line := range termtext.Wrap(val, v.c.Term.Cols-2-wide-2) {
			p := indent
			if j == 0 {
				p = prefix
			}
			io.WriteString(v.w, p+line+"\n")
		}
	}
}

func (v *view) body(src, format string) {
	if strings.TrimSpace(src) == "" {
		return
	}
	io.WriteString(v.w, "\n")
	for _, line := range strings.Split(strings.TrimRight(termtext.Render(src, format, v.opts()), "\n"), "\n") {
		if line == "" {
			io.WriteString(v.w, "\n")
			continue
		}
		io.WriteString(v.w, "  "+line+"\n")
	}
}

// event is one line for a system comment: its text without link
// targets, the time at the right edge at a terminal.
func (v *view) event(text, format, ts string) {
	line := "· " + termtext.Inline(text, format)
	when := v.c.when(ts)
	if cols := v.c.Term.Cols; cols > 0 {
		room := cols - 2 - 2 - cells(when)
		line = pad(clip(line, room), room)
	}
	io.WriteString(v.w, "  "+v.c.Term.paint(sgrDim, line+"  "+when)+"\n")
}

func (v *view) comment(author, ts, body, format string) {
	when := v.c.when(ts)
	cols := v.c.Term.Cols
	if cols > 0 {
		suffix := ", " + when + " "
		author = clip(author, max(0, cols-cells("── "+suffix)-1))
	}
	head := "── " + author + ", " + when + " "
	if cols > 0 {
		head += strings.Repeat("─", max(1, cols-cells(head)))
	}
	io.WriteString(v.w, "\n"+v.c.Term.paint(sgrDim, head)+"\n")
	v.body(body, format)
}
