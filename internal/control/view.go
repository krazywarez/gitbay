package control

import (
	"fmt"
	"io"
	"strings"

	"gitbay.org/gitbay/internal/store"
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

// view lays out a show piped: a title line, aligned fields, a body,
// events, comments. A terminal draws a screen instead (screen.go); a
// caller that still reaches view at a terminal gets the same plain
// layout, with user text made safe.
type view struct {
	c     *Ctx
	w     io.Writer
	wrote bool // has this view written anything yet
}

func (c *Ctx) view(w io.Writer) *view { return &view{c: c, w: w} }

func (v *view) opts() termtext.Options {
	return termtext.Options{Base: v.c.Cfg.Server.SiteURL}
}

// sep writes a blank line before the next block, unless this view has
// written nothing yet: fields, a body or a section as the first thing a
// command prints (admin runners, admin stats, notifications settings
// show, ...) does not open with an empty line.
func (v *view) sep() {
	if v.wrote {
		io.WriteString(v.w, "\n")
	}
	v.wrote = true
}

// section prints a sub-table's label: a blank line, then "label:".
// Callers skip the call entirely when the table it introduces has no
// rows.
func (v *view) section(label string) {
	v.sep()
	io.WriteString(v.w, label+":\n")
}

// title prints "ref  title  state" on one line. title or state may be
// "": either is skipped rather than leaving a trailing blank field.
func (v *view) title(ref, title, state string) {
	v.sep()
	t := v.c.Term
	ref, title, state = t.safe(ref), t.safe(title), t.safe(state)
	line := ref
	if title != "" {
		line += "  " + title
	}
	if state != "" {
		line += "  " + state
	}
	io.WriteString(v.w, line+"\n")
}

// fields prints key/value pairs aligned on the widest key, skipping
// empty values.
func (v *view) fields(kv ...string) {
	wide := 0
	for i := 0; i+1 < len(kv); i += 2 {
		if kv[i+1] != "" {
			wide = max(wide, cells(kv[i]))
		}
	}
	v.sep()
	for i := 0; i+1 < len(kv); i += 2 {
		key, val := v.c.Term.safe(kv[i]), v.c.Term.safe(kv[i+1])
		if val == "" {
			continue
		}
		io.WriteString(v.w, "  "+pad(key, wide)+"  "+val+"\n")
	}
}

func (v *view) body(src, format string) {
	if strings.TrimSpace(src) == "" {
		return
	}
	v.sep()
	src = v.c.Term.safe(src)
	for _, line := range strings.Split(strings.TrimRight(termtext.Render(src, format, v.opts()), "\n"), "\n") {
		if line == "" {
			io.WriteString(v.w, "\n")
			continue
		}
		io.WriteString(v.w, "  "+line+"\n")
	}
}

// text writes src as it is, indented: a commit message, which has no
// markup to render.
func (v *view) text(src string) {
	src = strings.TrimRight(v.c.Term.safe(src), "\n")
	if strings.TrimSpace(src) == "" {
		return
	}
	v.sep()
	for _, line := range strings.Split(src, "\n") {
		if line == "" {
			io.WriteString(v.w, "\n")
			continue
		}
		io.WriteString(v.w, "  "+line+"\n")
	}
}

// event is one line for a system comment: its text without link
// targets, then the time.
func (v *view) event(text, format, ts string) {
	line := "· " + termtext.Inline(v.c.Term.safe(text), format)
	io.WriteString(v.w, "  "+line+"  "+v.c.Term.safe(stamp(ts))+"\n")
	v.wrote = true
}

func (v *view) comment(id int64, author, ts, body, format string) {
	when := v.c.Term.safe(stamp(ts)) + fmt.Sprintf(" (comment %d)", id)
	head := "── " + v.c.Term.safe(author) + ", " + when + " "
	v.sep()
	io.WriteString(v.w, head+"\n")
	v.body(body, format)
}

// reactions prints one line of counts under an item, the caller's own
// marked "(you)". Nothing when nobody has reacted.
func (v *view) reactions(rs []ReactionOut) {
	if len(rs) == 0 {
		return
	}
	var parts []string
	for _, r := range rs {
		p := fmt.Sprintf("%s %d", store.ReactionEmoji(r.Reaction), r.Count)
		if r.Me {
			p += " (you)"
		}
		parts = append(parts, p)
	}
	io.WriteString(v.w, "reactions: "+strings.Join(parts, ", ")+"\n")
}
