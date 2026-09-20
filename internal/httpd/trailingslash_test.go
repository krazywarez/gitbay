package httpd

import (
	"bufio"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A path that only misses because of a trailing slash redirects to the
// path without it, query intact; one that misses either way is 404, and
// a POST is never redirected (#233).
func TestTrailingSlashRedirects(t *testing.T) {
	h := plainServer().Handler()
	for from, to := range map[string]string{
		"/cmc/":                   "/cmc",
		"/cmc/ccleberg/":          "/cmc/ccleberg",
		"/cmc/-/snippets/":        "/cmc/-/snippets",
		"/krz/gitbay/mrs/12/":     "/krz/gitbay/mrs/12",
		"/krz/gitbay/issues/?q=x": "/krz/gitbay/issues?q=x",
	} {
		w := get(t, h, from, nil)
		if w.Code != http.StatusMovedPermanently || w.Header().Get("Location") != to {
			t.Errorf("%s: %d %q, want 301 %q", from, w.Code, w.Header().Get("Location"), to)
		}
	}
	for _, p := range []string{"/", "/krz/gitbay/nothing/"} {
		if w := get(t, h, p, nil); p != "/" && w.Code != 404 {
			t.Errorf("%s: status %d, want 404", p, w.Code)
		}
	}
	// A Location must not leave the site: the mux cleans a leading //
	// before the fallback runs, and a backslash is escaped (#153).
	for _, p := range []string{"//evil.example/", "/\\evil.example/"} {
		w := get(t, h, p, nil)
		if loc := w.Header().Get("Location"); strings.HasPrefix(loc, "//") || strings.Contains(loc, "\\") {
			t.Errorf("%s: Location %q leaves the site", p, loc)
		}
	}
	// An absolute-form request line, which RFC 7230 requires a server to
	// accept, fills URL.Scheme and URL.Host. Neither may reach Location.
	// ReadRequest also takes Host from the URL; it is pinned back to the
	// site so this stays a test of the forge handler rather than of the
	// pages router.
	raw := "GET http://evil.example/cmc/ HTTP/1.1\r\nHost: forge.test\r\n\r\n"
	abs, err := http.ReadRequest(bufio.NewReader(strings.NewReader(raw)))
	if err != nil {
		t.Fatal(err)
	}
	abs.Host = "forge.test"
	aw := httptest.NewRecorder()
	h.ServeHTTP(aw, abs)
	if loc := aw.Header().Get("Location"); loc != "/cmc" {
		t.Errorf("absolute-form request: Location %q, want %q", loc, "/cmc")
	}

	r := httptest.NewRequest("POST", "/cmc/", nil)
	r.Host = "forge.test"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 404 {
		t.Errorf("POST /cmc/: status %d, want 404", w.Code)
	}
}
