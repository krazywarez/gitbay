package httpd

import (
	"html/template"
	"path"
	"regexp"
	"strings"
)

var (
	preTag  = regexp.MustCompile(`<pre\b[^>]*>`)
	imgTag  = regexp.MustCompile(`<img\b[^>]*>`)
	srcAttr = regexp.MustCompile(`\ssrc="([^"]*)"`)
)

// focusableBlocks gives every <pre> a tab stop. A block wider than its
// column scrolls sideways, and without one a keyboard has no way to reach
// the end of a long line (#232).
func focusableBlocks(h template.HTML) template.HTML {
	return template.HTML(preTag.ReplaceAllStringFunc(string(h), func(m string) string {
		if strings.Contains(m, "tabindex") {
			return m
		}
		return `<pre tabindex="0"` + m[len("<pre"):]
	}))
}

// imageAlt names an image that arrived without alt text after its file.
// go-org writes none for a bare image link, so a README badge was an image
// with no text inside a link with no name (#232). Markdown always carries
// alt, empty or not, and an empty alt is left alone: it means decorative.
func imageAlt(h template.HTML) template.HTML {
	return template.HTML(imgTag.ReplaceAllStringFunc(string(h), func(m string) string {
		if strings.Contains(m, " alt=") {
			return m
		}
		name := ""
		if sub := srcAttr.FindStringSubmatch(m); sub != nil {
			src := sub[1]
			if i := strings.IndexAny(src, "?#"); i >= 0 {
				src = src[:i]
			}
			name = path.Base(src)
			name = strings.TrimSuffix(name, path.Ext(name))
			if name == "." || name == "/" {
				name = ""
			}
		}
		return `<img alt="` + template.HTMLEscapeString(name) + `"` + m[len("<img"):]
	}))
}
