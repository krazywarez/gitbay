package termtext

import (
	"fmt"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	east "github.com/yuin/goldmark/extension/ast"
	"github.com/yuin/goldmark/text"
)

// md parses as the web does (CommonMark plus GFM); raw HTML is dropped
// there and here.
var md = goldmark.New(goldmark.WithExtensions(extension.GFM))

func Markdown(src string, o Options) string {
	return renderMarkdown(src, &out{o: o})
}

func renderMarkdown(src string, w *out) string {
	source := []byte(src)
	doc := md.Parser().Parse(text.NewReader(source))
	r := mdRenderer{w: w, src: source}
	r.blocks(doc, "", "")
	return w.String()
}

// Inline is src as one line of plain text, links reduced to their
// text, for event lines.
func Inline(src, format string) string {
	w := &out{noURLs: true}
	var s string
	if format == "org" {
		s = renderOrg(src, w)
	} else {
		s = renderMarkdown(src, w)
	}
	return strings.Join(strings.Fields(s), " ")
}

type mdRenderer struct {
	w   *out
	src []byte
}

// blocks renders n's children with a blank line between them. The
// first child's first line is prefixed by first, every other line by
// rest.
func (r mdRenderer) blocks(n ast.Node, first, rest string) {
	p := first
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		if c != n.FirstChild() {
			r.w.blank()
		}
		r.block(c, p, rest)
		p = rest
	}
}

func (r mdRenderer) block(n ast.Node, first, rest string) {
	switch n := n.(type) {
	case *ast.Heading:
		r.w.para(r.w.paint(sgrBold, r.inline(n)), first, rest)
	case *ast.Paragraph:
		r.w.para(r.inline(n), first, rest)
	case *ast.TextBlock:
		r.w.para(r.inline(n), first, rest)
	case *ast.List:
		i := n.Start
		p := first
		for item := n.FirstChild(); item != nil; item = item.NextSibling() {
			marker := "• "
			if n.IsOrdered() {
				marker = fmt.Sprintf("%d. ", i)
				i++
			}
			hang := rest + strings.Repeat(" ", cells(marker))
			for c := item.FirstChild(); c != nil; c = c.NextSibling() {
				if c == item.FirstChild() {
					r.block(c, p+marker, hang)
				} else {
					if !n.IsTight {
						r.w.blank()
					}
					r.block(c, hang, hang)
				}
			}
			p = rest
		}
	case *ast.FencedCodeBlock:
		r.w.code(r.lines(n), string(n.Language(r.src)), rest)
	case *ast.CodeBlock:
		r.w.code(r.lines(n), "", rest)
	case *ast.Blockquote:
		bar := r.w.paint(sgrDim, "│ ")
		r.blocks(n, first+bar, rest+bar)
	case *ast.ThematicBreak:
		r.w.rule(first)
	case *ast.HTMLBlock:
		// Dropped, as the web drops it.
	default:
		// GFM tables and anything else: the source, as a code block.
		// Tables (and their rows/cells) don't carry Lines() themselves,
		// so span the raw source under the node instead.
		r.w.code(r.raw(n), "", rest)
	}
}

func (r mdRenderer) lines(n ast.Node) string {
	var b strings.Builder
	ls := n.Lines()
	for i := 0; i < ls.Len(); i++ {
		seg := ls.At(i)
		b.Write(seg.Value(r.src))
	}
	return b.String()
}

// raw returns the source text spanned by n and its descendants,
// extended to whole lines.
func (r mdRenderer) raw(n ast.Node) string {
	start, end := -1, -1
	var walk func(ast.Node)
	walk = func(x ast.Node) {
		if x.Type() == ast.TypeBlock {
			ls := x.Lines()
			for i := 0; i < ls.Len(); i++ {
				seg := ls.At(i)
				if start == -1 || seg.Start < start {
					start = seg.Start
				}
				if seg.Stop > end {
					end = seg.Stop
				}
			}
		}
		for c := x.FirstChild(); c != nil; c = c.NextSibling() {
			walk(c)
		}
	}
	walk(n)
	if start == -1 {
		return ""
	}
	for start > 0 && r.src[start-1] != '\n' {
		start--
	}
	for end < len(r.src) && r.src[end] != '\n' {
		end++
	}
	return string(r.src[start:end])
}

func (r mdRenderer) inline(n ast.Node) string {
	var b strings.Builder
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		switch c := c.(type) {
		case *ast.Text:
			b.Write(c.Segment.Value(r.src))
			switch {
			case c.HardLineBreak():
				b.WriteString("\n")
			case c.SoftLineBreak():
				b.WriteString(" ")
			}
		case *ast.String:
			b.Write(c.Value)
		case *ast.CodeSpan:
			b.WriteString(r.inline(c))
		case *ast.Emphasis:
			sgr := sgrUnderline
			if c.Level == 2 {
				sgr = sgrBold
			}
			b.WriteString(r.w.paint(sgr, r.inline(c)))
		case *ast.Link:
			b.WriteString(r.w.link(r.inline(c), string(c.Destination)))
		case *ast.AutoLink:
			u := string(c.URL(r.src))
			b.WriteString(r.w.link(u, u))
		case *ast.Image:
			b.WriteString("[image: " + r.inline(c) + "]")
		case *ast.RawHTML:
		case *east.TaskCheckBox:
			if c.IsChecked {
				b.WriteString("[x] ")
			} else {
				b.WriteString("[ ] ")
			}
		default:
			b.WriteString(r.inline(c))
		}
	}
	return b.String()
}
