package httpd

import (
	"regexp"
	"strings"

	"gitbay.org/gitbay/internal/web"
)

// themedCSS derives the served stylesheet from style.css. The source keeps
// one dark token block under the media query, which the token tests read;
// served, that block is guarded so a page stamped data-theme="light" keeps
// the light tokens under a dark OS, and a copy of it applies, outside any
// media query, when the page is stamped data-theme="dark" (#232).
func themedCSS(src []byte) []byte {
	const open = "@media (prefers-color-scheme: dark) {\n  :root {"
	const close = "\n  }\n}"
	s := string(src)
	i := strings.Index(s, open)
	if i < 0 {
		return src
	}
	start := i + len(open)
	n := strings.Index(s[start:], close)
	if n < 0 {
		return src
	}
	body := s[start : start+n]
	var b strings.Builder
	b.WriteString(s[:i])
	b.WriteString("@media (prefers-color-scheme: dark) {\n  :root:not([data-theme=\"light\"]) {")
	b.WriteString(body)
	b.WriteString(close)
	b.WriteString("\n:root[data-theme=\"dark\"] {")
	b.WriteString(body)
	b.WriteString("\n}")
	b.WriteString(s[start+n+len(close):])
	return []byte(b.String())
}

// styleCSS is what /static/style.css serves, minus the syntax palettes.
var styleCSS = themedCSS(web.StyleCSS)

// chromaRule matches the start of one rule in chroma's generated CSS: an
// optional token-name comment, then the .chroma or .bg selector.
var chromaRule = regexp.MustCompile(`(?m)^((?:/\*[^*]*\*/ )?)(\.chroma|\.bg)`)

// scopeChroma prefixes every rule of a chroma palette with a selector, so
// the palette applies only under that scheme. The LineLink rule is dropped:
// it sets outline: none on the line-number links, which hid the focus ring;
// style.css carries the rest of it.
func scopeChroma(css, prefix string) string {
	var out []string
	for _, line := range strings.Split(css, "\n") {
		if strings.Contains(line, ".lnlinks") {
			continue
		}
		out = append(out, chromaRule.ReplaceAllString(line, "${1}"+prefix+" $2"))
	}
	return strings.Join(out, "\n")
}
