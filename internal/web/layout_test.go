package web

import (
	"strings"
	"testing"
)

// The repository header, main and footer share one centered container:
// the header's inner content is wrapped, and the stylesheet caps and
// centers all three on the same token (desktop layout spec).
func TestSharedCenteredContainer(t *testing.T) {
	layout, err := templateFS.ReadFile("templates/layout.html")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(layout), "<header class=\"repohead\">\n<div class=\"wrap\">") {
		t.Fatalf("repohead is not wrapped in .wrap")
	}
	css := string(StyleCSS)
	for _, want := range []string{
		"--container: 100rem;",
		"main.content, footer { max-width: calc(var(--container) + 2 * var(--sp-6)); margin: 0 auto; }",
		".repohead .wrap { max-width: var(--container); margin: 0 auto; }",
		"main.reading { max-width: calc(72rem + 2 * var(--sp-6)); margin: 0 auto; }",
		"main.bounded { max-width: calc(48rem + 2 * var(--sp-6)); margin: 0 auto; }",
	} {
		if !strings.Contains(css, want) {
			t.Errorf("style.css lacks %q", want)
		}
	}
}
