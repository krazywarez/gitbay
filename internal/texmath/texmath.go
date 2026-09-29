// Package texmath converts a subset of TeX math to MathML.
//
// The subset: letters, numbers and operator characters; ^ and _; braces;
// \frac and friends, \binom, \sqrt with an optional index; Greek letters and
// the common symbols, relations and arrows in symbols.go; function names
// (\sin, \lim, \operatorname{...}); large operators, which take their limits
// above and below in display style; \left, \middle and \right; accents,
// \overline, \underline and the braces; spacing commands; \text; the font
// commands \mathrm, \mathbf, \mathbb, \mathcal, \mathscr, \mathfrak, \mathsf,
// \mathtt, \mathit, \boldsymbol; and the environments matrix, pmatrix,
// bmatrix, Bmatrix, vmatrix, Vmatrix, smallmatrix, cases, aligned, align,
// gathered, gather, split, equation and displaymath.
//
// Anything else, including every command that defines macros, sets colors or
// styles, or links, is an error; the caller shows the source instead. The
// output uses only the elements and attributes listed in Elements and Attrs,
// and its size is linear in the input, which is capped at MaxInput bytes and
// MaxDepth levels of nesting.
package texmath

import (
	"errors"
	"fmt"
	"html"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Limits on one expression.
const (
	MaxInput = 8 << 10
	MaxDepth = 64
)

// Elements is every MathML element Convert emits.
var Elements = []string{
	"math", "mrow", "mi", "mn", "mo", "mtext", "mspace",
	"mfrac", "msqrt", "mroot", "msub", "msup", "msubsup",
	"munder", "mover", "munderover", "mtable", "mtr", "mtd",
}

// Attrs is every attribute Convert emits, by element, with the values it
// can take: a fixed value, or "<length>" for an em length.
var Attrs = map[string]map[string]string{
	"math":   {"display": "block"},
	"mi":     {"mathvariant": "normal"},
	"mo":     {"stretchy": "false"},
	"mfrac":  {"linethickness": "0"},
	"mover":  {"accent": "true"},
	"mspace": {"width": "<length>"},
}

var (
	ErrTooLong = errors.New("texmath: expression too long")
	ErrTooDeep = errors.New("texmath: expression nested too deeply")
)

// Convert renders tex as one <math> element; display selects block layout
// and display-style limits.
func Convert(tex string, display bool) (string, error) {
	if len(tex) > MaxInput {
		return "", ErrTooLong
	}
	if !utf8.ValidString(tex) {
		return "", errors.New("texmath: invalid UTF-8")
	}
	p := &parser{src: tex, display: display}
	kids, err := p.list(func(t token) bool { return false })
	if err != nil {
		return "", err
	}
	if t := p.peek(); t.kind != eof {
		return "", p.unexpected(t)
	}
	root := &node{tag: "math", kids: kids}
	if display {
		root.attr("display", "block")
	}
	var b strings.Builder
	root.write(&b)
	return b.String(), nil
}

type node struct {
	tag    string
	attrs  [][2]string
	text   string
	kids   []*node
	limits bool // a large operator: limits go under and over in display style
	fn     bool // a function name: a thin space follows unless a delimiter does
}

func (n *node) attr(k, v string) *node {
	n.attrs = append(n.attrs, [2]string{k, v})
	return n
}

func (n *node) write(b *strings.Builder) {
	b.WriteByte('<')
	b.WriteString(n.tag)
	for _, a := range n.attrs {
		b.WriteString(" " + a[0] + `="` + html.EscapeString(a[1]) + `"`)
	}
	b.WriteByte('>')
	b.WriteString(html.EscapeString(n.text))
	for _, k := range n.kids {
		k.write(b)
	}
	b.WriteString("</" + n.tag + ">")
}

func el(tag string, kids ...*node) *node { return &node{tag: tag, kids: kids} }
func leaf(tag, text string) *node        { return &node{tag: tag, text: text} }
func row(kids []*node) *node             { return &node{tag: "mrow", kids: kids} }
func space(w string) *node               { return (&node{tag: "mspace"}).attr("width", w) }

// fixed is an operator that must not stretch to the height of its row, the
// way a bracket typed without \left does not in TeX.
func fixed(s string) *node { return leaf("mo", s).attr("stretchy", "false") }

type kind int

const (
	eof  kind = iota
	char      // one character
	cmd       // a control sequence; val is its name without the backslash
)

type token struct {
	kind kind
	val  string
	pos  int
}

type parser struct {
	src     string
	pos     int
	depth   int
	display bool
	font    string
}

func (p *parser) skip() {
	for p.pos < len(p.src) {
		c := p.src[p.pos]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			p.pos++
		case c == '%':
			for p.pos < len(p.src) && p.src[p.pos] != '\n' {
				p.pos++
			}
		default:
			return
		}
	}
}

func (p *parser) peek() token {
	save := p.pos
	t := p.next()
	p.pos = save
	return t
}

func (p *parser) next() token {
	p.skip()
	start := p.pos
	if p.pos >= len(p.src) {
		return token{kind: eof, pos: start}
	}
	if p.src[p.pos] == '\\' {
		p.pos++
		if p.pos >= len(p.src) {
			return token{kind: cmd, val: "", pos: start}
		}
		if isASCIILetter(p.src[p.pos]) {
			end := p.pos
			for end < len(p.src) && isASCIILetter(p.src[end]) {
				end++
			}
			name := p.src[p.pos:end]
			p.pos = end
			// \operatorname* and friends: the star is part of the name.
			if p.pos < len(p.src) && p.src[p.pos] == '*' && starred[name] {
				p.pos++
				name += "*"
			}
			return token{kind: cmd, val: name, pos: start}
		}
		r, n := utf8.DecodeRuneInString(p.src[p.pos:])
		p.pos += n
		return token{kind: cmd, val: string(r), pos: start}
	}
	r, n := utf8.DecodeRuneInString(p.src[p.pos:])
	p.pos += n
	return token{kind: char, val: string(r), pos: start}
}

var starred = map[string]bool{"operatorname": true}

func isASCIILetter(c byte) bool { return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }

func (p *parser) unexpected(t token) error {
	switch t.kind {
	case eof:
		return errors.New("texmath: unexpected end of input")
	case cmd:
		return fmt.Errorf(`texmath: unexpected \%s at %d`, t.val, t.pos)
	}
	return fmt.Errorf("texmath: unexpected %q at %d", t.val, t.pos)
}

func (p *parser) expect(kind kind, val string) error {
	if t := p.next(); t.kind != kind || t.val != val {
		return p.unexpected(t)
	}
	return nil
}

func isChar(t token, s string) bool { return t.kind == char && t.val == s }
func isCmd(t token, s string) bool  { return t.kind == cmd && t.val == s }

// list parses atoms until the input ends or stop matches the next token,
// which is left unread.
func (p *parser) list(stop func(token) bool) ([]*node, error) {
	var out []*node
	for {
		t := p.peek()
		if t.kind == eof || stop(t) {
			return out, nil
		}
		n, err := p.scripted()
		if err != nil {
			return nil, err
		}
		if n == nil {
			continue
		}
		out = append(out, n)
		if n.fn && !p.delimiterNext() {
			out = append(out, space("0.1667em"))
		}
	}
}

// delimiterNext reports whether the next token closes a group or opens a
// bracket, where TeX puts no space after a function name.
func (p *parser) delimiterNext() bool {
	t := p.peek()
	if t.kind == eof {
		return true
	}
	if t.kind == char {
		return strings.Contains("()[]{}|&.,;", t.val)
	}
	switch t.val {
	case "left", "right", "\\", "end", ",", ";", "!", "quad", "qquad", "{", "}":
		return true
	}
	return false
}

// scripted parses one atom and any sub- and superscripts attached to it.
func (p *parser) scripted() (*node, error) {
	var base *node
	if t := p.peek(); !isChar(t, "^") && !isChar(t, "_") {
		var err error
		if base, err = p.atom(); err != nil {
			return nil, err
		}
	}
	var sub, sup *node
	limits := base != nil && base.limits && p.display
	for {
		t := p.peek()
		switch {
		case isCmd(t, "limits"):
			p.next()
			limits = base != nil && base.limits
			continue
		case isCmd(t, "nolimits"):
			p.next()
			limits = false
			continue
		case isChar(t, "^"), isChar(t, "_"):
		default:
			if sub == nil && sup == nil {
				return base, nil
			}
			if base == nil {
				base = row(nil)
			}
			return script(base, sub, sup, limits), nil
		}
		p.next()
		arg, err := p.arg()
		if err != nil {
			return nil, err
		}
		if t.val == "^" {
			if sup != nil {
				return nil, fmt.Errorf("texmath: double superscript at %d", t.pos)
			}
			sup = arg
		} else {
			if sub != nil {
				return nil, fmt.Errorf("texmath: double subscript at %d", t.pos)
			}
			sub = arg
		}
	}
}

func script(base, sub, sup *node, limits bool) *node {
	fn := base.fn
	var n *node
	switch {
	case limits && sub != nil && sup != nil:
		n = el("munderover", base, sub, sup)
	case limits && sub != nil:
		n = el("munder", base, sub)
	case limits:
		n = el("mover", base, sup)
	case sub != nil && sup != nil:
		n = el("msubsup", base, sub, sup)
	case sub != nil:
		n = el("msub", base, sub)
	default:
		n = el("msup", base, sup)
	}
	n.fn = fn
	return n
}

// arg parses a command's argument: a braced group, or else one token.
func (p *parser) arg() (*node, error) {
	t := p.peek()
	switch {
	case t.kind == eof:
		return nil, p.unexpected(t)
	case t.kind == char && strings.Contains("}&^_", t.val):
		return nil, p.unexpected(t)
	case t.kind == char && t.val >= "0" && t.val <= "9":
		p.next()
		return leaf("mn", p.styled(t.val)), nil
	}
	return p.atom()
}

func (p *parser) enter() error {
	p.depth++
	if p.depth > MaxDepth {
		return ErrTooDeep
	}
	return nil
}

// atom parses one atom. A nil node with a nil error is a command that
// renders nothing, such as \displaystyle.
func (p *parser) atom() (*node, error) {
	if err := p.enter(); err != nil {
		return nil, err
	}
	defer func() { p.depth-- }()
	t := p.next()
	switch t.kind {
	case eof:
		return nil, p.unexpected(t)
	case char:
		return p.charAtom(t)
	}
	return p.command(t)
}

func (p *parser) charAtom(t token) (*node, error) {
	r, _ := utf8.DecodeRuneInString(t.val)
	switch {
	case t.val == "{":
		font := p.font
		kids, err := p.list(func(t token) bool { return isChar(t, "}") })
		p.font = font
		if err != nil {
			return nil, err
		}
		if err := p.expect(char, "}"); err != nil {
			return nil, err
		}
		return row(kids), nil
	case r >= '0' && r <= '9':
		num := t.val
		for p.pos < len(p.src) {
			c := p.src[p.pos]
			if c >= '0' && c <= '9' || c == '.' && p.pos+1 < len(p.src) && p.src[p.pos+1] >= '0' && p.src[p.pos+1] <= '9' {
				num += string(c)
				p.pos++
				continue
			}
			break
		}
		return leaf("mn", p.styled(num)), nil
	case unicode.IsLetter(r):
		n := leaf("mi", p.styled(t.val))
		if p.font == "rm" {
			n.attr("mathvariant", "normal")
		}
		return n, nil
	}
	switch t.val {
	case "}", "&", "$", "#", "\\":
		return nil, p.unexpected(t)
	case "(", ")", "[", "]", "|", "/":
		return fixed(t.val), nil
	case "-":
		return leaf("mo", "−"), nil
	case "*":
		return leaf("mo", "∗"), nil
	case "'":
		return leaf("mo", "′"), nil
	case "~":
		return space("0.3333em"), nil
	}
	if r < 0x20 || r == utf8.RuneError {
		return nil, p.unexpected(t)
	}
	return leaf("mo", t.val), nil
}

func (p *parser) command(t token) (*node, error) {
	name := t.val
	if s, ok := symbols[name]; ok {
		n := leaf(s.tag, s.text)
		if s.upright {
			n.attr("mathvariant", "normal")
		}
		if s.fixed {
			n.attr("stretchy", "false")
		}
		n.limits = s.limits
		return n, nil
	}
	if w, ok := spaces[name]; ok {
		return space(w), nil
	}
	if f, ok := functions[name]; ok {
		n := leaf("mi", name)
		n.limits, n.fn = f, true
		return n, nil
	}
	if a, ok := accents[name]; ok {
		arg, err := p.arg()
		if err != nil {
			return nil, err
		}
		if a.under {
			return el("munder", arg, leaf("mo", a.mark)), nil
		}
		n := el("mover", arg, leaf("mo", a.mark))
		if a.accent {
			n.attr("accent", "true")
		}
		return n, nil
	}
	if f, ok := fonts[name]; ok {
		font := p.font
		p.font = f
		arg, err := p.arg()
		p.font = font
		return arg, err
	}
	switch name {
	case "frac", "dfrac", "tfrac", "cfrac":
		num, err := p.arg()
		if err != nil {
			return nil, err
		}
		den, err := p.arg()
		if err != nil {
			return nil, err
		}
		return el("mfrac", num, den), nil
	case "binom", "dbinom", "tbinom":
		top, err := p.arg()
		if err != nil {
			return nil, err
		}
		bottom, err := p.arg()
		if err != nil {
			return nil, err
		}
		frac := el("mfrac", top, bottom).attr("linethickness", "0")
		return row([]*node{leaf("mo", "("), frac, leaf("mo", ")")}), nil
	case "sqrt":
		var index *node
		if isChar(p.peek(), "[") {
			p.next()
			kids, err := p.list(func(t token) bool { return isChar(t, "]") })
			if err != nil {
				return nil, err
			}
			if err := p.expect(char, "]"); err != nil {
				return nil, err
			}
			index = row(kids)
		}
		arg, err := p.arg()
		if err != nil {
			return nil, err
		}
		if index != nil {
			return el("mroot", arg, index), nil
		}
		return el("msqrt", arg), nil
	case "left":
		open, err := p.delimiter()
		if err != nil {
			return nil, err
		}
		kids, err := p.list(func(t token) bool { return isCmd(t, "right") })
		if err != nil {
			return nil, err
		}
		if err := p.expect(cmd, "right"); err != nil {
			return nil, err
		}
		cls, err := p.delimiter()
		if err != nil {
			return nil, err
		}
		var out []*node
		if open != "" {
			out = append(out, leaf("mo", open))
		}
		out = append(out, kids...)
		if cls != "" {
			out = append(out, leaf("mo", cls))
		}
		return row(out), nil
	case "middle":
		d, err := p.delimiter()
		if err != nil {
			return nil, err
		}
		return leaf("mo", d), nil
	case "text", "textrm", "textnormal", "mbox":
		s, err := p.rawGroup()
		if err != nil {
			return nil, err
		}
		return leaf("mtext", s), nil
	case "operatorname", "operatorname*":
		s, err := p.rawGroup()
		if err != nil {
			return nil, err
		}
		n := leaf("mi", s)
		if utf8.RuneCountInString(s) == 1 {
			n.attr("mathvariant", "normal")
		}
		n.limits, n.fn = name == "operatorname*", true
		return n, nil
	case "begin":
		return p.environment()
	case "displaystyle", "textstyle", "scriptstyle", "scriptscriptstyle", "limits", "nolimits":
		return nil, nil
	}
	return nil, p.unexpected(t)
}

// rawGroup reads a braced argument as text, for \text and \operatorname.
// Nested braces must balance; \{, \}, \$, \%, \&, \#, \_ and \\ stand for
// the character.
func (p *parser) rawGroup() (string, error) {
	if err := p.expect(char, "{"); err != nil {
		return "", err
	}
	var b strings.Builder
	depth := 0
	for p.pos < len(p.src) {
		c := p.src[p.pos]
		switch {
		case c == '\\' && p.pos+1 < len(p.src) && strings.IndexByte(`{}$%&#_\`, p.src[p.pos+1]) >= 0:
			b.WriteByte(p.src[p.pos+1])
			p.pos += 2
			continue
		case c == '{':
			depth++
		case c == '}':
			if depth == 0 {
				p.pos++
				return b.String(), nil
			}
			depth--
		}
		b.WriteByte(c)
		p.pos++
	}
	return "", errors.New("texmath: unterminated group")
}

// delimiter reads what follows \left, \middle or \right; "." is none.
func (p *parser) delimiter() (string, error) {
	t := p.next()
	if t.kind == char {
		switch t.val {
		case ".":
			return "", nil
		case "<":
			return "⟨", nil
		case ">":
			return "⟩", nil
		case "(", ")", "[", "]", "|", "/":
			return t.val, nil
		}
	}
	if t.kind == cmd {
		if d, ok := delimiters[t.val]; ok {
			return d, nil
		}
	}
	return "", p.unexpected(t)
}

type env struct {
	open, close string
	table       bool
}

var environments = map[string]env{
	"matrix":      {table: true},
	"smallmatrix": {table: true},
	"pmatrix":     {"(", ")", true},
	"bmatrix":     {"[", "]", true},
	"Bmatrix":     {"{", "}", true},
	"vmatrix":     {"|", "|", true},
	"Vmatrix":     {"‖", "‖", true},
	"cases":       {"{", "", true},
	"aligned":     {table: true},
	"align":       {table: true},
	"align*":      {table: true},
	"gathered":    {table: true},
	"gather":      {table: true},
	"gather*":     {table: true},
	"split":       {table: true},
	"equation":    {},
	"equation*":   {},
	"displaymath": {},
}

func (p *parser) envName() (string, error) {
	if err := p.expect(char, "{"); err != nil {
		return "", err
	}
	end := strings.IndexByte(p.src[p.pos:], '}')
	if end < 0 {
		return "", errors.New("texmath: unterminated environment name")
	}
	name := p.src[p.pos : p.pos+end]
	p.pos += end + 1
	return name, nil
}

func (p *parser) environment() (*node, error) {
	name, err := p.envName()
	if err != nil {
		return nil, err
	}
	e, ok := environments[name]
	if !ok {
		return nil, fmt.Errorf("texmath: unsupported environment %q", name)
	}
	var body *node
	if e.table {
		body, err = p.table()
	} else {
		var kids []*node
		kids, err = p.list(func(t token) bool { return isCmd(t, "end") })
		body = row(kids)
	}
	if err != nil {
		return nil, err
	}
	if err := p.expect(cmd, "end"); err != nil {
		return nil, err
	}
	if end, err := p.envName(); err != nil {
		return nil, err
	} else if end != name {
		return nil, fmt.Errorf(`texmath: \begin{%s} ended by \end{%s}`, name, end)
	}
	if e.open == "" && e.close == "" {
		return body, nil
	}
	out := []*node{}
	if e.open != "" {
		out = append(out, leaf("mo", e.open))
	}
	out = append(out, body)
	if e.close != "" {
		out = append(out, leaf("mo", e.close))
	}
	return row(out), nil
}

// table parses rows separated by \\ and cells separated by &, up to \end.
func (p *parser) table() (*node, error) {
	stop := func(t token) bool { return isChar(t, "&") || isCmd(t, "\\") || isCmd(t, "end") }
	tbl := el("mtable")
	cur := el("mtr")
	for {
		kids, err := p.list(stop)
		if err != nil {
			return nil, err
		}
		cur.kids = append(cur.kids, el("mtd", kids...))
		t := p.peek()
		switch {
		case isChar(t, "&"):
			p.next()
		case isCmd(t, "\\"):
			p.next()
			tbl.kids = append(tbl.kids, cur)
			cur = el("mtr")
		case isCmd(t, "end"):
			// A trailing \\ leaves one empty cell; it is not a row.
			if len(cur.kids) > 1 || len(cur.kids[0].kids) > 0 {
				tbl.kids = append(tbl.kids, cur)
			}
			return tbl, nil
		default:
			return nil, p.unexpected(t)
		}
	}
}

// styled maps letters and digits into the current font's Mathematical
// Alphanumeric Symbols, which is how MathML Core spells \mathbb and the rest:
// mathvariant is honoured only as "normal".
func (p *parser) styled(s string) string {
	if p.font == "" || p.font == "rm" {
		return s
	}
	var b strings.Builder
	for _, r := range s {
		b.WriteRune(alphanumeric(p.font, r))
	}
	return b.String()
}
