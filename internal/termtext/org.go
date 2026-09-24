package termtext

import (
	"bytes"
	"errors"
	"io"
	"log"
	"strings"

	"github.com/niklasfasching/go-org/org"
)

func Org(src string, o Options) string {
	return renderOrg(src, &out{o: o})
}

// renderOrg parses with the same restrictions as the web: no file is
// ever read (#+INCLUDE, #+SETUPFILE), and parse warnings go nowhere.
func renderOrg(src string, w *out) string {
	c := org.New()
	c.ReadFile = func(string) ([]byte, error) { return nil, errors.New("org: includes are disabled") }
	c.Log = log.New(io.Discard, "", 0)
	doc := c.Parse(bytes.NewReader([]byte(src)), "")
	r := orgRenderer{w: w}
	r.nodes(doc.Nodes, "", "")
	return w.String()
}

type orgRenderer struct{ w *out }

func (r orgRenderer) nodes(ns []org.Node, first, rest string) {
	p := first
	wrote := false
	for _, n := range ns {
		if skipOrg(n) {
			continue
		}
		if wrote {
			r.w.blank()
		}
		r.block(n, p, rest)
		p, wrote = rest, true
	}
}

// skipOrg drops nodes that carry nothing to render: keywords
// (#+INCLUDE included — ReadFile already refuses the read, but the
// keyword itself is still a line of source, not content), property
// drawers, comments, and the empty paragraph go-org emits for a blank
// line that separated two blocks or ended a list item.
func skipOrg(n org.Node) bool {
	switch n := n.(type) {
	case org.Keyword, org.Include, *org.PropertyDrawer, org.Comment:
		return true
	case org.Paragraph:
		return len(n.Children) == 0
	}
	return false
}

func (r orgRenderer) block(n org.Node, first, rest string) {
	switch n := n.(type) {
	case org.Headline:
		r.w.para(r.w.paint(sgrBold, r.inline(n.Title)), first, rest)
		if len(n.Children) > 0 {
			r.w.blank()
			r.nodes(n.Children, rest, rest)
		}
	case org.Paragraph:
		r.w.para(r.inline(n.Children), first, rest)
	case org.List:
		loose := looseOrgList(n)
		p := first
		for i, item := range n.Items {
			if i > 0 && loose {
				r.w.blank()
			}
			switch item := item.(type) {
			case org.ListItem:
				marker := "• "
				if n.Kind == "ordered" {
					marker = item.Bullet + " "
				}
				hang := rest + strings.Repeat(" ", cells(marker))
				r.itemChildren(item.Children, p+marker, hang, loose)
			case org.DescriptiveListItem:
				term := r.w.paint(sgrBold, r.inline(item.Term))
				r.w.para(term, p, rest)
				r.itemChildren(item.Details, rest+"  ", rest+"  ", loose)
			default:
				r.w.code(org.String(item), "", rest)
			}
			p = rest
		}
	case org.Block:
		switch strings.ToUpper(n.Name) {
		case "SRC":
			lang := ""
			if len(n.Parameters) > 0 {
				lang = n.Parameters[0]
			}
			r.w.code(org.String(n.Children...), lang, rest)
		case "QUOTE":
			bar := r.w.paint(sgrDim, "│ ")
			r.nodes(n.Children, first+bar, rest+bar)
		default:
			r.w.code(org.String(n.Children...), "", rest)
		}
	case org.Example:
		r.w.code(org.String(n.Children...), "", rest)
	case org.HorizontalRule:
		r.w.rule(first)
	default:
		r.w.code(org.String(n), "", rest)
	}
}

// looseOrgList reports whether n's source had a blank line between
// any two of its items. go-org marks the item before such a blank
// with a trailing empty org.Paragraph — the same artifact skipOrg
// drops elsewhere, checked here first since skipOrg would erase it.
// The list's own last item gets that trailing empty paragraph too
// whenever a blank line follows the whole list (ending it before the
// next block), which says nothing about spacing inside the list, so
// only non-last items are checked. A single-item list is always
// tight by this measure: there is no gap between items to have or
// lack a blank line.
func looseOrgList(n org.List) bool {
	for i := 0; i < len(n.Items)-1; i++ {
		var children []org.Node
		switch item := n.Items[i].(type) {
		case org.ListItem:
			children = item.Children
		case org.DescriptiveListItem:
			children = item.Details
		default:
			continue
		}
		if len(children) == 0 {
			continue
		}
		if p, ok := children[len(children)-1].(org.Paragraph); ok && len(p.Children) == 0 {
			return true
		}
	}
	return false
}

// itemChildren renders a list item's children. A loose list keeps
// nodes' blank line between them; a tight list runs them straight
// together, so a nested list sits directly under its parent item's
// line rather than a line below it.
func (r orgRenderer) itemChildren(ns []org.Node, first, rest string, loose bool) {
	if loose {
		r.nodes(ns, first, rest)
		return
	}
	p := first
	for _, n := range ns {
		if skipOrg(n) {
			continue
		}
		r.block(n, p, rest)
		p = rest
	}
}

// trimOrgLineBreaks drops leading and trailing org.LineBreak nodes: a
// paragraph that follows a keyword across a blank line, or a blank
// line gap already handled elsewhere, otherwise renders that break as
// the leading or trailing space inline() gives it.
func trimOrgLineBreaks(ns []org.Node) []org.Node {
	isBreak := func(n org.Node) bool { _, ok := n.(org.LineBreak); return ok }
	for len(ns) > 0 && isBreak(ns[0]) {
		ns = ns[1:]
	}
	for len(ns) > 0 && isBreak(ns[len(ns)-1]) {
		ns = ns[:len(ns)-1]
	}
	return ns
}

func (r orgRenderer) inline(ns []org.Node) string {
	ns = trimOrgLineBreaks(ns)
	var b strings.Builder
	for _, n := range ns {
		switch n := n.(type) {
		case org.Text:
			b.WriteString(n.Content)
		case org.LineBreak:
			b.WriteString(" ")
		case org.ExplicitLineBreak:
			b.WriteString("\n")
		case org.Emphasis:
			s := r.inline(n.Content)
			switch n.Kind {
			case "*":
				s = r.w.paint(sgrBold, s)
			case "/", "_":
				s = r.w.paint(sgrUnderline, s)
			}
			b.WriteString(s)
		case org.RegularLink:
			b.WriteString(r.w.link(r.inline(n.Description), n.URL))
		default:
			b.WriteString(org.String(n))
		}
	}
	return b.String()
}
