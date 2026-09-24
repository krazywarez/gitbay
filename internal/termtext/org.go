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

// skipOrg drops nodes that carry nothing to render: keywords, property
// drawers, comments, and the empty paragraph go-org emits for a blank
// line that separated two blocks or ended a list item.
func skipOrg(n org.Node) bool {
	switch n := n.(type) {
	case org.Keyword, *org.PropertyDrawer, org.Comment:
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
		p := first
		for i, item := range n.Items {
			if i > 0 && n.Kind == "descriptive" {
				r.w.blank()
			}
			switch item := item.(type) {
			case org.ListItem:
				marker := "• "
				if n.Kind == "ordered" {
					marker = item.Bullet + " "
				}
				hang := rest + strings.Repeat(" ", cells(marker))
				r.nodes(item.Children, p+marker, hang)
			case org.DescriptiveListItem:
				term := r.w.paint(sgrBold, r.inline(item.Term))
				r.w.para(term, p, rest)
				r.nodes(item.Details, rest+"  ", rest+"  ")
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

func (r orgRenderer) inline(ns []org.Node) string {
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
