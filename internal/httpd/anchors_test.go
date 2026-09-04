package httpd

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gitbay.org/gitbay/internal/config"
)

// Markdown headings carry ids, so a README section can be deep-linked
// the way org headings already could (#132).
func TestMarkdownHeadingAnchors(t *testing.T) {
	out := string(renderReadme("README.md", []byte("# Getting started\n\n## Two words\n\n- [ ] a task\n")))
	for _, want := range []string{`id="getting-started"`, `id="two-words"`, `type="checkbox"`} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered markdown lacks %s:\n%s", want, out)
		}
	}
}

// The stylesheet carries an ETag and a cache lifetime; a revalidation
// with the same tag is a 304 with no body (#132).
func TestStylesheetRevalidates(t *testing.T) {
	s := New(config.Default(), nil)
	first := httptest.NewRecorder()
	s.stylesheet(first, httptest.NewRequest("GET", "/static/style.css", nil))
	tag := first.Header().Get("ETag")
	if first.Code != 200 || tag == "" || first.Body.Len() == 0 || !strings.Contains(first.Header().Get("Cache-Control"), "max-age") {
		t.Fatalf("first fetch: %d etag=%q cc=%q bytes=%d", first.Code, tag, first.Header().Get("Cache-Control"), first.Body.Len())
	}
	req := httptest.NewRequest("GET", "/static/style.css", nil)
	req.Header.Set("If-None-Match", tag)
	second := httptest.NewRecorder()
	s.stylesheet(second, req)
	if second.Code != http.StatusNotModified || second.Body.Len() != 0 {
		t.Fatalf("revalidation: %d with %d bytes", second.Code, second.Body.Len())
	}
}
