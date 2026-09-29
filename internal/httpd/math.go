package httpd

import (
	"bytes"
	"html/template"
	"regexp"
	"strings"

	"github.com/microcosm-cc/bluemonday"
	"github.com/niklasfasching/go-org/org"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"

	"gitbay.org/gitbay/internal/texmath"
)

// TeX math renders server-side as MathML, which browsers display natively,
// so the page needs no script and the CSP does not change (#294).
//
// The converter is internal/texmath rather than a library. The pure-Go
// options were treeblood (MIT), which writes \color and \class arguments
// into attributes unescaped, expands \def macros without a bound (a 180-byte
// input produced 3 MB), did not finish 5000 nested braces in 30 seconds and
// logs to stderr; goldmark-mathml, which runs Temml in a JavaScript VM; and
// converters inside large typesetting modules. texmath covers a documented
// subset, refuses everything else, and bounds input size and nesting.

// mathHTML renders one expression, or escapes its source when the converter
// refuses it. The result passes through ugcPolicy even on the markdown path,
// so the policy is the one statement of what math may emit.
func mathHTML(tex, source string, display bool) (string, bool) {
	out, err := texmath.Convert(tex, display)
	if err != nil {
		return template.HTMLEscapeString(source), false
	}
	return ugcPolicy.Sanitize(out), true
}

// allowMath admits exactly the MathML texmath emits: its elements, and each
// attribute only on its element and only with the values it writes.
func allowMath(p *bluemonday.Policy) {
	p.AllowElements(texmath.Elements...)
	p.AllowNoAttrs().OnElements(texmath.Elements...)
	for element, attrs := range texmath.Attrs {
		for name, value := range attrs {
			pattern := `^` + regexp.QuoteMeta(value) + `$`
			if value == "<length>" {
				pattern = `^-?[0-9]+(\.[0-9]+)?em$`
			}
			p.AllowAttrs(name).Matching(regexp.MustCompile(pattern)).OnElements(element)
		}
	}
}

// Markdown: $…$ inline and $$…$$ display, inline or as a block.

var (
	kindMath      = ast.NewNodeKind("Math")
	kindMathBlock = ast.NewNodeKind("MathBlock")
)

type mathInline struct {
	ast.BaseInline
	tex     string
	display bool
}

func (n *mathInline) Kind() ast.NodeKind { return kindMath }
func (n *mathInline) Dump(src []byte, level int) {
	ast.DumpHelper(n, src, level, map[string]string{"TeX": n.tex}, nil)
}

type mathBlock struct {
	ast.BaseBlock
	tex    []byte
	closed bool
}

func (n *mathBlock) Kind() ast.NodeKind { return kindMathBlock }
func (n *mathBlock) Dump(src []byte, level int) {
	ast.DumpHelper(n, src, level, map[string]string{"TeX": string(n.tex)}, nil)
}

func isMathSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' }

// mathInlineParser follows pandoc's rule so prices stay prose: the opening
// $ has a non-space after it, and the closing $ a non-space before it and no
// digit after it. "$5 and $10" is text. A backslash escapes the next byte.
type mathInlineParser struct{}

func (mathInlineParser) Trigger() []byte { return []byte{'$'} }

func (mathInlineParser) Parse(parent ast.Node, block text.Reader, pc parser.Context) ast.Node {
	line, seg := block.PeekLine()
	if len(line) >= 2 && line[1] == '$' {
		body := line[2:]
		for i := 0; i+1 < len(body); i++ {
			if body[i] == '\\' {
				i++
				continue
			}
			if body[i] == '$' && body[i+1] == '$' {
				if i == 0 {
					break
				}
				block.Advance(i + 4)
				return &mathInline{tex: string(body[:i]), display: true}
			}
		}
		// Consume both dollars so the second does not open inline math.
		block.Advance(2)
		return ast.NewTextSegment(seg.WithStop(seg.Start + 2))
	}
	body := line[1:]
	if len(body) == 0 || isMathSpace(body[0]) {
		return nil
	}
	for i := 0; i < len(body); i++ {
		switch body[i] {
		case '\\':
			i++
		case '$':
			if isMathSpace(body[i-1]) || i+1 < len(body) && body[i+1] >= '0' && body[i+1] <= '9' {
				continue
			}
			block.Advance(i + 2)
			return &mathInline{tex: string(body[:i])}
		}
	}
	return nil
}

// mathBlockParser opens on a line that is $$ alone, or $$…$$ whole, and
// runs to the line that ends with $$.
type mathBlockParser struct{}

func (mathBlockParser) Trigger() []byte { return []byte{'$'} }

func (mathBlockParser) Open(parent ast.Node, reader text.Reader, pc parser.Context) (ast.Node, parser.State) {
	line, _ := reader.PeekLine()
	pos := pc.BlockOffset()
	if pos < 0 || !bytes.HasPrefix(line[pos:], []byte("$$")) {
		return nil, parser.NoChildren
	}
	rest := util.TrimRightSpace(line[pos+2:])
	n := &mathBlock{}
	switch {
	case len(rest) == 0:
	case len(rest) > 2 && bytes.HasSuffix(rest, []byte("$$")):
		n.tex, n.closed = rest[:len(rest)-2], true
	default:
		return nil, parser.NoChildren
	}
	reader.AdvanceToEOL()
	return n, parser.NoChildren
}

func (mathBlockParser) Continue(node ast.Node, reader text.Reader, pc parser.Context) parser.State {
	n := node.(*mathBlock)
	if n.closed {
		return parser.Close
	}
	line, _ := reader.PeekLine()
	if line == nil {
		return parser.Close
	}
	trimmed := util.TrimRightSpace(line)
	if bytes.HasSuffix(trimmed, []byte("$$")) {
		n.tex = append(n.tex, trimmed[:len(trimmed)-2]...)
		n.closed = true
		reader.AdvanceToEOL()
		return parser.Close
	}
	n.tex = append(n.tex, line...)
	reader.AdvanceToEOL()
	return parser.Continue | parser.NoChildren
}

func (mathBlockParser) Close(ast.Node, text.Reader, parser.Context) {}
func (mathBlockParser) CanInterruptParagraph() bool                 { return true }
func (mathBlockParser) CanAcceptIndentedLine() bool                 { return false }

type mathRenderer struct{}

func (mathRenderer) RegisterFuncs(reg renderer.NodeRendererFuncRegisterer) {
	reg.Register(kindMath, func(w util.BufWriter, _ []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
		if entering {
			n := node.(*mathInline)
			delim := "$"
			if n.display {
				delim = "$$"
			}
			out, _ := mathHTML(n.tex, delim+n.tex+delim, n.display)
			_, _ = w.WriteString(out)
		}
		return ast.WalkSkipChildren, nil
	})
	reg.Register(kindMathBlock, func(w util.BufWriter, _ []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		n := node.(*mathBlock)
		source := "$$" + string(n.tex)
		if n.closed {
			source += "$$"
		}
		out, ok := mathHTML(string(n.tex), source, true)
		if !ok || !n.closed {
			out = "<pre>" + template.HTMLEscapeString(source) + "</pre>"
		}
		_, _ = w.WriteString(out + "\n")
		return ast.WalkSkipChildren, nil
	})
}

type mathExtension struct{}

func (mathExtension) Extend(m goldmark.Markdown) {
	m.Parser().AddOptions(
		parser.WithBlockParsers(util.Prioritized(mathBlockParser{}, 701)),
		parser.WithInlineParsers(util.Prioritized(mathInlineParser{}, 501)))
	m.Renderer().AddOptions(renderer.WithNodeRenderers(util.Prioritized(mathRenderer{}, 500)))
}

// Org: go-org already parses $…$, $$…$$, \(…\), \[…\] and \begin{…}…\end{…}
// as LaTeX fragments, and \begin{…} on its own lines as a LaTeX block; it
// writes them back out as text. These render them instead.

func (w *orgWriter) WriteLatexFragment(l org.LatexFragment) {
	tex := org.String(l.Content...)
	source := l.OpeningPair + tex + l.ClosingPair
	// go-org takes any $…$; org's own rule keeps "$5 and $10" prose.
	if l.OpeningPair == "$" && (tex == "" || isMathSpace(tex[0]) || isMathSpace(tex[len(tex)-1])) {
		w.WriteText(org.Text{Content: source, IsRaw: true})
		return
	}
	display := l.OpeningPair != "$" && l.OpeningPair != `\(`
	if strings.HasPrefix(l.OpeningPair, `\begin{`) {
		tex = source
	}
	out, ok := mathHTML(tex, source, display)
	if !ok {
		w.WriteText(org.Text{Content: source, IsRaw: true})
		return
	}
	w.WriteString(out)
}

func (w *orgWriter) WriteLatexBlock(b org.LatexBlock) {
	tex := org.String(b.Content...)
	if out, ok := mathHTML(tex, tex, true); ok {
		w.WriteString(out + "\n")
		return
	}
	w.WriteString("<pre>" + template.HTMLEscapeString(tex) + "</pre>\n")
}
