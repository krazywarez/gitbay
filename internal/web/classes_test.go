package web

import (
	"io/fs"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// TestEveryTemplateClassHasARule fails on a class a template uses that
// no selector in style.css mentions. Class tokens containing template
// actions ({{...}}) are composed at render time and skipped; their
// prefixes (chip-, badge-, check-, lang-) are covered by the rules for
// the concrete values.
func TestEveryTemplateClassHasARule(t *testing.T) {
	css := string(StyleCSS)
	// Every ".name" that appears in a selector position: outside braces.
	depth := 0
	var sel strings.Builder
	for _, r := range css {
		switch r {
		case '{':
			depth++
		case '}':
			depth--
		default:
			if depth == 0 {
				sel.WriteRune(r)
			}
		}
	}
	// Media queries wrap rules one level deeper; strip their headers and
	// scan again at depth one.
	depth = 0
	inMedia := false
	for i := 0; i < len(css); i++ {
		if strings.HasPrefix(css[i:], "@media") {
			inMedia = true
		}
		switch css[i] {
		case '{':
			depth++
			if depth == 1 && !inMedia {
				// ordinary rule; already scanned above
			}
		case '}':
			depth--
			if depth == 0 {
				inMedia = false
			}
		default:
			if inMedia && depth == 1 {
				sel.WriteByte(css[i])
			}
		}
	}
	classRe := regexp.MustCompile(`\.([a-zA-Z_][a-zA-Z0-9_-]*)`)
	styled := map[string]bool{}
	for _, m := range classRe.FindAllStringSubmatch(sel.String(), -1) {
		styled[m[1]] = true
	}

	attrRe := regexp.MustCompile(`class="([^"]*)"`)
	missing := map[string][]string{}
	entries, err := fs.ReadDir(templateFS, "templates")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		src, err := templateFS.ReadFile("templates/" + e.Name())
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range attrRe.FindAllStringSubmatch(string(src), -1) {
			for _, c := range strings.Fields(m[1]) {
				if strings.Contains(c, "{{") || strings.Contains(c, "}}") {
					continue
				}
				if !styled[c] {
					missing[c] = append(missing[c], e.Name())
				}
			}
		}
	}
	var names []string
	for c := range missing {
		names = append(names, c)
	}
	sort.Strings(names)
	for _, c := range names {
		t.Errorf("class %q in %s has no rule in style.css", c, strings.Join(uniq(missing[c]), ", "))
	}
}

func uniq(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
