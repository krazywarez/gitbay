package httpd

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gitbay.org/gitbay/internal/config"
	"gitbay.org/gitbay/internal/store"
	"gitbay.org/gitbay/internal/web"
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

// The layout stamps the served bytes' hash on the stylesheet URL, so a
// deploy changes that URL and a browser cannot answer it from a copy of
// the old sheet. A URL carrying the hash names bytes that cannot change
// and is served without revalidation; the bare URL keeps its lifetime
// (#239).
func TestStylesheetURLCarriesTheBuildHash(t *testing.T) {
	var sb strings.Builder
	var base basePage
	base.Viewer = "alice"
	err := web.Render(&sb, "dashboard.html", struct {
		basePage
		Tab      string
		Pins     []pinnedRow
		Reviews  []store.DashboardItem
		Assigned []store.DashboardItem
		MRs      []store.DashboardItem
		Issues   []store.DashboardItem
		Feed     []feedLine
	}{base, "dashboard", nil, nil, nil, nil, nil, nil})
	if err != nil {
		t.Fatal(err)
	}
	want := `href="/static/style.css?v=` + stylesheetHash + `"`
	if !strings.Contains(sb.String(), want) {
		t.Errorf("the page does not link %s", want)
	}

	s := New(config.Default(), nil)
	versioned := httptest.NewRecorder()
	s.stylesheet(versioned, httptest.NewRequest("GET", "/static/style.css?v="+stylesheetHash, nil))
	if cc := versioned.Header().Get("Cache-Control"); !strings.Contains(cc, "immutable") {
		t.Errorf("versioned URL: Cache-Control = %q, want immutable", cc)
	}
	bare := httptest.NewRecorder()
	s.stylesheet(bare, httptest.NewRequest("GET", "/static/style.css", nil))
	if cc := bare.Header().Get("Cache-Control"); !strings.Contains(cc, "must-revalidate") {
		t.Errorf("bare URL: Cache-Control = %q, want must-revalidate", cc)
	}
}
