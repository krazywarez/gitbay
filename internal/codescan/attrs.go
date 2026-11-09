package codescan

import "strings"

// unquotedAttrRule flags a template expression inside a tag unless it sees
// the expression within ="…" or ='…'. It matches in generic mode, where a
// quoted string ends at the next quote, so href="{{ f("a") }}" is reported,
// and so is an action that only adds an attribute name
// ({{if .X}} selected{{end}}). unquotedLines reads the template as HTML to
// tell those apart from the real thing.
const unquotedAttrRule = "html-templates.security.unquoted-attribute-var"

// rawText are elements whose content is not markup.
var rawText = map[string]bool{"script": true, "style": true, "textarea": true, "title": true}

// Tag states.
const (
	inText = iota
	beforeAttr
	attrName
	afterName
	beforeValue
	unquotedValue
	quotedValue
)

// unquotedLines returns the lines of an HTML template where an output
// expression is an unquoted attribute value or stands in for attributes.
// ok is false when the file does not read as HTML to the end, and then
// nothing is known.
func unquotedLines(src []byte) (lines map[int]bool, ok bool) {
	s := string(src)
	lines = map[int]bool{}
	line := 1
	state := inText
	var quote byte
	var tag string
	advance := func(to int, from int) int {
		line += strings.Count(s[from:to], "\n")
		return to
	}
	for i := 0; i < len(s); {
		if end, output, found := expression(s, i); found {
			if end < 0 {
				return nil, false
			}
			if output && state != inText && state != quotedValue {
				lines[line] = true
				if state == beforeValue {
					state = unquotedValue
				} else if state != unquotedValue {
					state = attrName
				}
			}
			i = advance(end, i)
			continue
		}
		c, prev := s[i], state
		switch state {
		case inText:
			switch {
			case strings.HasPrefix(s[i:], "<!--"):
				end := strings.Index(s[i+4:], "-->")
				if end < 0 {
					return nil, false
				}
				i = advance(i+4+end+3, i)
				continue
			case c == '<' && i+1 < len(s) && isLetter(s[i+1]):
				j := i + 1
				for j < len(s) && (isLetter(s[j]) || s[j] >= '0' && s[j] <= '9' || s[j] == '-' || s[j] == ':') {
					j++
				}
				tag = strings.ToLower(s[i+1 : j])
				state = beforeAttr
				i = j
				continue
			}
		case quotedValue:
			if c == quote {
				state = beforeAttr
			}
		case beforeValue:
			switch {
			case c == '"' || c == '\'':
				quote, state = c, quotedValue
			case c == '>':
				state = inText
			case !isSpace(c):
				state = unquotedValue
			}
		case unquotedValue:
			if isSpace(c) {
				state = beforeAttr
			} else if c == '>' {
				state = inText
			}
		default: // beforeAttr, attrName, afterName
			switch {
			case c == '=':
				state = beforeValue
			case c == '>':
				state = inText
			case isSpace(c):
				if state == attrName {
					state = afterName
				}
			case c != '/':
				state = attrName
			}
		}
		i = advance(i+1, i)
		if prev != inText && state == inText && rawText[tag] && s[i-2] != '/' {
			end := strings.Index(strings.ToLower(s[i:]), "</"+tag)
			if end < 0 {
				return nil, false
			}
			i = advance(i+end, i)
		}
	}
	return lines, state == inText
}

// expression reports whether a template expression opens at i: {{ }},
// {{{ }}}, {% %} or {# #}. end is just past its close, or -1 when it is
// not closed. output is whether it writes a value, rather than being a
// control action or a comment.
func expression(s string, i int) (end int, output, found bool) {
	open, close := "", ""
	switch {
	case strings.HasPrefix(s[i:], "{{{"):
		open, close = "{{{", "}}}"
	case strings.HasPrefix(s[i:], "{{"):
		open, close = "{{", "}}"
	case strings.HasPrefix(s[i:], "{%"):
		open, close = "{%", "%}"
	case strings.HasPrefix(s[i:], "{#"):
		open, close = "{#", "#}"
	default:
		return 0, false, false
	}
	n := strings.Index(s[i+len(open):], close)
	if n < 0 {
		return -1, false, true
	}
	if open[1] != '{' {
		return i + len(open) + n + len(close), false, true
	}
	body := strings.TrimLeft(s[i+len(open):i+len(open)+n], "-~ \t\r\n")
	return i + len(open) + n + len(close), !control(body), true
}

// control reports whether the body of a {{ }} is a Go template action or a
// Handlebars block, comment or else, which write nothing themselves.
func control(body string) bool {
	if body == "" || strings.ContainsRune("#/^!", rune(body[0])) {
		return true
	}
	word, _, _ := strings.Cut(strings.TrimSpace(body), " ")
	switch word {
	case "if", "else", "end", "range", "with", "block", "define", "break", "continue":
		return true
	}
	return false
}

func isLetter(c byte) bool { return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }

func isSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f' }
