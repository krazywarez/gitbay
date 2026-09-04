package httpd

import (
	"regexp"
	"strings"
	"testing"

	"gitbay.org/gitbay/internal/web"
)

var (
	inputTag   = regexp.MustCompile(`<input\b[^>]*>`)
	labelFor   = regexp.MustCompile(`<label[^>]*\bfor="([^"]+)"`)
	labelWraps = regexp.MustCompile(`(?s)<label\b[^>]*>.*?</label>`)
	attrID     = regexp.MustCompile(`\bid="([^"]+)"`)
	attrType   = regexp.MustCompile(`\btype="([^"]+)"`)
	ariaLabel  = regexp.MustCompile(`\baria-label(?:ledby)?="`)
)

// Every input a person types into needs an accessible name: a <label for>
// pointing at its id, or an aria-label. A placeholder is not one — it
// disappears on focus and screen readers are not required to announce it.
//
// #133 fixed this class across the templates and missed the topics-remove
// field, which the first SonarCloud scan then found
// (Web:InputWithoutLabelCheck, #153). This is the guard that was absent.
func TestEveryInputHasAnAccessibleName(t *testing.T) {
	// Types that carry their own name or take no input.
	exempt := map[string]bool{"hidden": true, "submit": true, "button": true, "reset": true, "image": true}
	for _, name := range web.Pages() {
		src, err := web.TemplateSource(name)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		labelled := map[string]bool{}
		for _, m := range labelFor.FindAllStringSubmatch(src, -1) {
			labelled[m[1]] = true
		}
		// `<label>Name <input></label>` associates implicitly and is as
		// good as `for`/`id`. Six inputs use it, and a check that knew
		// only the explicit form would report every one of them.
		wrapped := labelWraps.FindAllStringIndex(src, -1)
		inWrappedLabel := func(at int) bool {
			for _, span := range wrapped {
				if at >= span[0] && at < span[1] {
					return true
				}
			}
			return false
		}
		for _, at := range inputTag.FindAllStringIndex(src, -1) {
			tag := src[at[0]:at[1]]
			typ := "text"
			if m := attrType.FindStringSubmatch(tag); m != nil {
				typ = m[1]
			}
			if exempt[typ] || ariaLabel.MatchString(tag) || inWrappedLabel(at[0]) {
				continue
			}
			m := attrID.FindStringSubmatch(tag)
			if m == nil {
				t.Errorf("%s: input has no id and is not inside a <label>: %s", name, strings.TrimSpace(tag))
				continue
			}
			if !labelled[m[1]] {
				t.Errorf("%s: input id=%q has no <label for>: %s", name, m[1], strings.TrimSpace(tag))
			}
		}
	}
}
