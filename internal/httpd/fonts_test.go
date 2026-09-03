package httpd

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"

	"gitbay.org/gitbay/internal/config"
	"gitbay.org/gitbay/internal/web"
)

// TestStylesheetFontsAreServed: every font URL the embedded stylesheet asks
// for has a route, and that route answers with the file. The route names
// and the @font-face URLs were once maintained by hand and drifted, so
// gitbay.org served no web font at all (#102).
func TestStylesheetFontsAreServed(t *testing.T) {
	s := New(config.Default(), nil)
	byPattern := map[string]http.HandlerFunc{}
	for _, r := range s.Routes() {
		if r.Method == "GET" {
			byPattern[r.Pattern] = r.Handler
		}
	}
	urls := regexp.MustCompile(`url\((/static/fonts/[^)]+)\)`).FindAllSubmatch(web.StyleCSS, -1)
	if len(urls) == 0 {
		t.Fatal("stylesheet declares no font URLs")
	}
	for _, m := range urls {
		u := string(m[1])
		h, ok := byPattern[u]
		if !ok {
			t.Errorf("%s: stylesheet asks for it, route table has no route", u)
			continue
		}
		rec := httptest.NewRecorder()
		h(rec, httptest.NewRequest("GET", u, nil))
		if rec.Code != http.StatusOK || rec.Body.Len() == 0 {
			t.Errorf("%s: status %d, %d bytes", u, rec.Code, rec.Body.Len())
		}
		if ct := rec.Header().Get("Content-Type"); ct != "font/woff2" {
			t.Errorf("%s: content-type %q", u, ct)
		}
	}
}
