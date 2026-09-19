package httpd

import (
	"strings"
	"testing"

	"gitbay.org/gitbay/internal/web"
)

// The served stylesheet guards the dark media block against an explicit
// light choice and repeats it, verbatim, for an explicit dark one (#232).
func TestThemedCSSCarriesBothDarkBlocks(t *testing.T) {
	css := string(themedCSS(web.StyleCSS))
	const guarded = "@media (prefers-color-scheme: dark) {\n  :root:not([data-theme=\"light\"]) {"
	const stamped = "\n:root[data-theme=\"dark\"] {"
	gi, si := strings.Index(css, guarded), strings.Index(css, stamped)
	if gi < 0 || si < 0 {
		t.Fatalf("guarded %d stamped %d", gi, si)
	}
	body := func(from int) string {
		rest := css[from:]
		return rest[:strings.Index(rest, "\n  }\n}")]
	}
	guardedBody := body(gi + len(guarded))
	rest := css[si+len(stamped):]
	stampedBody := rest[:strings.Index(rest, "\n}")]
	if guardedBody != stampedBody {
		t.Fatalf("dark blocks differ:\n%s\n---\n%s", guardedBody, stampedBody)
	}
	if !strings.Contains(guardedBody, "--canvas: #101114;") {
		t.Fatalf("dark block lacks the canvas token:\n%s", guardedBody)
	}
	if strings.Contains(css, "@media (prefers-color-scheme: dark) {\n  :root {") {
		t.Fatal("the dark token block is served unguarded")
	}
	// The source keeps the plain block, which the token tests parse.
	if !strings.Contains(string(web.StyleCSS), "@media (prefers-color-scheme: dark) {\n  :root {") {
		t.Fatal("style.css no longer has the plain dark block")
	}
}

// Each syntax palette is served four ways: under its media query unless
// the other theme is stamped, and unconditionally under its own stamp.
// chroma's LineLink rule, which set outline: none, is not served at all.
func TestChromaPalettesAreScoped(t *testing.T) {
	css := string(chromaCSS)
	for _, want := range []string{
		`:root:not([data-theme="dark"]) .chroma .na { color: #6f5a21 }`,
		`:root:not([data-theme="light"]) .chroma `,
		`:root[data-theme="light"] .chroma `,
		`:root[data-theme="dark"] .chroma `,
		`:root[data-theme="dark"] .bg `,
	} {
		if !strings.Contains(css, want) {
			t.Errorf("lacks %s", want)
		}
	}
	if strings.Contains(css, "lnlinks") {
		t.Error("chroma's LineLink rule is served")
	}
	if strings.Contains(css, "\n.chroma .k ") {
		t.Error("an unscoped palette rule is served")
	}
}
