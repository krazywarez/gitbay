// Package termtext renders markdown and org to text for a terminal:
// wrapped to a width, links reduced to their text, code highlighted
// with 16 colours. Width 0 is plain: no wrapping and no SGR, for
// piped output.
package termtext

import (
	"bytes"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/alecthomas/chroma/v2/quick"
	"golang.org/x/text/width"
)

type Options struct {
	Width int
	Color bool
	Base  string
}

func Render(src, format string, o Options) string {
	if format == "org" {
		return Org(src, o)
	}
	return Markdown(src, o)
}

const (
	sgrReset     = "\x1b[0m"
	sgrBold      = "\x1b[1m"
	sgrDim       = "\x1b[2m"
	sgrUnderline = "\x1b[4m"
)

// out collects rendered lines. Every block goes through it so the
// prefixes (indent, list marker, quote bar) and the wrap live in one
// place.
type out struct {
	o      Options
	b      strings.Builder
	noURLs bool // links as their text only (Inline)
}

func (w *out) paint(sgr, s string) string {
	if !w.o.Color || s == "" {
		return s
	}
	return sgr + s + sgrReset
}

// para writes s wrapped to the width, the first line after first and
// the rest after rest. Hard breaks in s ("\n") start a new line. An
// SGR run open at a wrapped line's end is closed there and reopened
// after the next line's prefix, so the prefix itself is never painted
// and a style never bleeds past a line break.
func (w *out) para(s, first, rest string) {
	prefix := first
	var open []string
	for _, hard := range strings.Split(s, "\n") {
		for _, line := range Wrap(hard, w.o.Width-cells(rest)) {
			w.b.WriteString(prefix)
			for _, sgr := range open {
				w.b.WriteString(sgr)
			}
			w.b.WriteString(line)
			open = sgrOpen(line, open)
			if len(open) > 0 {
				w.b.WriteString(sgrReset)
			}
			w.b.WriteString("\n")
			prefix = rest
		}
	}
}

// sgrOpen scans s for SGR sequences, starting from the stack of runs
// already open, and returns the stack still open at s's end. sgrReset
// clears the whole stack; any other sequence pushes onto it.
func sgrOpen(s string, open []string) []string {
	for i := 0; i < len(s); i++ {
		if s[i] != 0x1b {
			continue
		}
		j := strings.IndexByte(s[i:], 'm')
		if j < 0 {
			break
		}
		sgr := s[i : i+j+1]
		if sgr == sgrReset {
			open = nil
		} else {
			open = append(open, sgr)
		}
		i += j
	}
	return open
}

// code writes lines verbatim under prefix plus four spaces,
// highlighted when colour is on.
func (w *out) code(src, lang, prefix string) {
	src = strings.TrimRight(src, "\n")
	if w.o.Color && w.o.Width > 0 {
		var hb bytes.Buffer
		if lang == "" {
			lang = "plaintext"
		}
		if quick.Highlight(&hb, src, lang, "terminal16", "monokai") == nil {
			src = strings.TrimRight(hb.String(), "\n")
		}
	}
	for _, line := range strings.Split(src, "\n") {
		w.b.WriteString(prefix + "    " + line + "\n")
	}
}

func (w *out) rule(prefix string) {
	w.b.WriteString(prefix + w.paint(sgrDim, "───") + "\n")
}

func (w *out) blank() { w.b.WriteString("\n") }

func (w *out) String() string {
	return strings.TrimRight(w.b.String(), "\n") + "\n"
}

// link is a link as terminal text: its text, then the target when the
// target says something the text does not. Relative targets are made
// absolute against Base.
func (w *out) link(text, target string) string {
	if w.noURLs && text != "" {
		return text
	}
	if strings.HasPrefix(target, "/") && w.o.Base != "" {
		target = strings.TrimRight(w.o.Base, "/") + target
	}
	if text == "" {
		return target
	}
	if target == "" || target == text || "mailto:"+text == target ||
		strings.TrimPrefix(strings.TrimPrefix(target, "https://"), "http://") == text {
		return text
	}
	return text + " (" + target + ")"
}

// Wrap breaks s at spaces into lines of at most width cells. A word
// wider than width is a line of its own. width <= 0 is no wrapping.
func Wrap(s string, width int) []string {
	if width <= 0 {
		return []string{s}
	}
	var lines []string
	var cur string
	for _, word := range strings.Fields(s) {
		switch {
		case cur == "":
			cur = word
		case cells(cur)+1+cells(word) <= width:
			cur += " " + word
		default:
			lines = append(lines, cur)
			cur = word
		}
	}
	if cur != "" || len(lines) == 0 {
		lines = append(lines, cur)
	}
	return lines
}

func cells(s string) int {
	n := 0
	for i := 0; i < len(s); {
		if s[i] == 0x1b {
			j := strings.IndexByte(s[i:], 'm')
			if j < 0 {
				break
			}
			i += j + 1
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		i += size
		switch {
		case unicode.In(r, unicode.Mn, unicode.Me) || r == '‍':
		case width.LookupRune(r).Kind() == width.EastAsianWide || width.LookupRune(r).Kind() == width.EastAsianFullwidth:
			n += 2
		default:
			n++
		}
	}
	return n
}

func stripSGR(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == 0x1b {
			if j := strings.IndexByte(s[i:], 'm'); j >= 0 {
				i += j
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}
