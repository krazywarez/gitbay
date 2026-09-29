package httpd

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"html/template"
	"regexp"
	"sort"
	"strconv"
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
//
// MathML reaches a page only from the converter. ugcPolicy, which cleans
// user-authored HTML, admits none of it; mathPolicy admits exactly what
// texmath emits and cleans each converted expression.

// mathPolicy admits the MathML texmath emits: its elements, and each
// attribute only on its element and only with the values it writes.
var mathPolicy = func() *bluemonday.Policy {
	p := bluemonday.NewPolicy()
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
	return p
}()

// mathHTML renders one expression, or escapes its source when the converter
// refuses it.
func mathHTML(tex, source string, display bool) (string, bool) {
	out, err := texmath.Convert(tex, display)
	if err != nil {
		return template.HTMLEscapeString(source), false
	}
	return mathPolicy.Sanitize(out), true
}

// Per document, math stops rendering after this many expressions or this
// much TeX; later delimiters stay literal text.
const (
	maxMathExprs = 1000
	maxMathBytes = 256 << 10
)

type mathBudget struct{ n, bytes int }

func (b *mathBudget) take(size int) bool {
	if b.n >= maxMathExprs || b.bytes+size > maxMathBytes {
		return false
	}
	b.n++
	b.bytes += size
	return true
}

// Markdown: $…$ inline and $$…$$ display, inline or as a block.

var (
	kindMath      = ast.NewNodeKind("Math")
	kindMathBlock = ast.NewNodeKind("MathBlock")

	mathBudgetKey = parser.NewContextKey()
	mathLineKey   = parser.NewContextKey()
)

func budgetFor(pc parser.Context) *mathBudget {
	b, _ := pc.Get(mathBudgetKey).(*mathBudget)
	if b == nil {
		b = &mathBudget{}
		pc.Set(mathBudgetKey, b)
	}
	return b
}

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
	source []byte
	closed bool
}

func (n *mathBlock) Kind() ast.NodeKind { return kindMathBlock }
func (n *mathBlock) Dump(src []byte, level int) {
	ast.DumpHelper(n, src, level, map[string]string{"TeX": string(n.tex)}, nil)
}

func isMathSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' }

// mathLine is the valid closing dollars of one line, as source offsets,
// found in one pass so each opener on the line looks its closer up rather
// than rescanning the rest of the line.
type mathLine struct {
	stop    int
	closers []int
}

// closers scans line, which starts at an opening $, for dollars that can
// close inline math: a non-space before, no digit after, not escaped.
func closers(line []byte, base int) []int {
	var out []int
	for i := 1; i < len(line); i++ {
		switch line[i] {
		case '\\':
			i++
		case '$':
			if isMathSpace(line[i-1]) || i+1 < len(line) && line[i+1] >= '0' && line[i+1] <= '9' {
				continue
			}
			out = append(out, base+i)
		}
	}
	return out
}

// mathInlineParser follows pandoc's rule so prices stay prose: the opening
// $ has a non-space after it, and the closing $ a non-space before it and no
// digit after it. "$5 and $10" is text. A backslash escapes the next byte.
type mathInlineParser struct{}

func (mathInlineParser) Trigger() []byte { return []byte{'$'} }

func (mathInlineParser) Parse(parent ast.Node, block text.Reader, pc parser.Context) ast.Node {
	line, seg := block.PeekLine()
	if len(line) >= 2 && line[1] == '$' {
		// Scanning stops at the next $$, where the next attempt starts,
		// so each byte is scanned at most twice.
		body := line[2:]
		for i := 0; i+1 < len(body); i++ {
			if body[i] == '\\' {
				i++
				continue
			}
			if body[i] == '$' && body[i+1] == '$' {
				if i == 0 || !budgetFor(pc).take(i) {
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
	if len(line) < 2 || isMathSpace(line[1]) {
		return nil
	}
	start := seg.Start - seg.Padding
	cache, _ := pc.Get(mathLineKey).(*mathLine)
	if cache == nil || cache.stop != seg.Stop {
		cache = &mathLine{stop: seg.Stop, closers: closers(line, start)}
		pc.Set(mathLineKey, cache)
	}
	k := sort.SearchInts(cache.closers, start+2)
	if k == len(cache.closers) {
		return nil
	}
	end := cache.closers[k] - start
	if !budgetFor(pc).take(end - 1) {
		return nil
	}
	block.Advance(end + 1)
	return &mathInline{tex: string(line[1:end])}
}

// mathBlockParser opens on a line that is $$ alone, when a line ending in
// $$ follows before a blank line, or on a line that is $$…$$ whole.
// Anything else stays paragraph text.
type mathBlockParser struct{}

func (mathBlockParser) Trigger() []byte { return []byte{'$'} }

func (mathBlockParser) Open(parent ast.Node, reader text.Reader, pc parser.Context) (ast.Node, parser.State) {
	line, seg := reader.PeekLine()
	pos := pc.BlockOffset()
	if pos < 0 || !bytes.HasPrefix(line[pos:], []byte("$$")) {
		return nil, parser.NoChildren
	}
	rest := util.TrimRightSpace(line[pos+2:])
	n := &mathBlock{}
	switch {
	case len(rest) == 0:
		// The lookahead stops at the first line ending in $$, which the
		// block then consumes, so no line is looked at twice by it.
		// It reads the source rather than moving the reader, whose
		// SetPosition keeps the line it last peeked.
		size, found := 0, false
		src := reader.Source()
		for i := seg.Stop; i < len(src); {
			end := len(src)
			if j := bytes.IndexByte(src[i:], '\n'); j >= 0 {
				end = i + j + 1
			}
			next := src[i:end]
			if util.IsBlank(next) {
				break
			}
			size += len(next)
			if bytes.HasSuffix(util.TrimRightSpace(next), []byte("$$")) {
				found = true
				break
			}
			i = end
		}
		if !found || !budgetFor(pc).take(size) {
			return nil, parser.NoChildren
		}
		n.source = append(n.source, "$$\n"...)
	case len(rest) > 2 && bytes.HasSuffix(rest, []byte("$$")):
		if !budgetFor(pc).take(len(rest) - 2) {
			return nil, parser.NoChildren
		}
		n.tex, n.closed = rest[:len(rest)-2], true
		n.source = append([]byte("$$"), rest...)
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
	if line == nil || util.IsBlank(line) {
		return parser.Close
	}
	trimmed := util.TrimRightSpace(line)
	if bytes.HasSuffix(trimmed, []byte("$$")) {
		n.tex = append(n.tex, trimmed[:len(trimmed)-2]...)
		n.source = append(n.source, trimmed...)
		n.closed = true
		reader.AdvanceToEOL()
		return parser.Close
	}
	n.tex = append(n.tex, line...)
	n.source = append(n.source, line...)
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
		source := string(n.source)
		out, ok := mathHTML(string(n.tex), source, true)
		if !ok || !n.closed {
			// A container that ended before the closing line leaves the
			// block unclosed; show what it held.
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
//
// The whole org document goes through ugcPolicy, which admits no MathML, so
// the writer leaves a placeholder for each expression and fill puts the
// mathPolicy-cleaned MathML back after sanitizing. The placeholder carries a
// random per-render prefix, so a document cannot spell one.

type mathSlots struct {
	prefix string
	html   []string
	source []string
	budget mathBudget
}

func newMathSlots() *mathSlots {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return &mathSlots{prefix: "gitbaymath" + hex.EncodeToString(b[:]) + "n"}
}

func (m *mathSlots) put(html, source string) string {
	m.html = append(m.html, html)
	m.source = append(m.source, source)
	return m.prefix + strconv.Itoa(len(m.html)-1) + "z"
}

// fill replaces each placeholder in sanitized HTML with its MathML, or with
// its escaped source where the placeholder landed inside a tag (an
// attribute value). Sanitized output escapes < and > everywhere but in
// tags, so the last of them seen says whether the text is inside one.
func (m *mathSlots) fill(doc string) string {
	if len(m.html) == 0 {
		return doc
	}
	var b strings.Builder
	inTag := false
	for {
		i := strings.Index(doc, m.prefix)
		if i < 0 {
			b.WriteString(doc)
			return b.String()
		}
		before := doc[:i]
		if j := strings.LastIndexAny(before, "<>"); j >= 0 {
			inTag = before[j] == '<'
		}
		b.WriteString(before)
		doc = doc[i+len(m.prefix):]
		end := strings.IndexByte(doc, 'z')
		if end < 0 {
			b.WriteString(m.prefix)
			continue
		}
		k, err := strconv.Atoi(doc[:end])
		if err != nil || k < 0 || k >= len(m.html) {
			b.WriteString(m.prefix)
			continue
		}
		doc = doc[end+1:]
		if inTag {
			b.WriteString(template.HTMLEscapeString(m.source[k]))
		} else {
			b.WriteString(m.html[k])
		}
	}
}

// writeMath writes an expression's placeholder, or its source as text when
// the converter refuses it or the document's budget is spent.
func (w *orgWriter) writeMath(tex, source string, display bool) {
	if w.math.budget.take(len(tex)) {
		if out, ok := mathHTML(tex, source, display); ok {
			w.WriteString(w.math.put(out, source))
			return
		}
	}
	w.WriteText(org.Text{Content: source, IsRaw: true})
}

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
	w.writeMath(tex, source, display)
}

func (w *orgWriter) WriteLatexBlock(b org.LatexBlock) {
	tex := org.String(b.Content...)
	if w.math.budget.take(len(tex)) {
		if out, ok := mathHTML(tex, tex, true); ok {
			w.WriteString(w.math.put(out, tex) + "\n")
			return
		}
	}
	w.WriteString("<pre>" + template.HTMLEscapeString(tex) + "</pre>\n")
}
