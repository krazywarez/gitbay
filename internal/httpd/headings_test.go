package httpd

import (
	"strings"
	"testing"
)

// A rendered README sits under the page's own h1, so its headings move
// down one level, ids intact; h6 stays h6 (#133).
func TestDemoteHeadings(t *testing.T) {
	out := string(renderReadme("README.md", []byte("# Top\n\n## Next\n\n###### Deep\n")))
	for _, want := range []string{`<h2 id="top">Top</h2>`, `<h3 id="next">Next</h3>`, `<h6 id="deep">Deep</h6>`} {
		if !strings.Contains(out, want) {
			t.Errorf("lacks %s:\n%s", want, out)
		}
	}
	if strings.Contains(out, "<h1") {
		t.Errorf("an h1 survived:\n%s", out)
	}
}
