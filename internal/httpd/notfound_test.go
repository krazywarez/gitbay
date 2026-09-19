package httpd

import (
	"strings"
	"testing"
)

// A path no route matches renders the 404 page, the same one a missing
// record gets, instead of net/http's plain-text body (#232).
func TestUnmatchedPathRendersNotFoundPage(t *testing.T) {
	h := plainServer().Handler()
	// One and two segment paths are owner and repository routes, which
	// need a store; these fall past every pattern.
	for _, p := range []string{"/krz/gitbay/mrs/315/files", "/krz/gitbay/nothing/at/all"} {
		w := get(t, h, p, nil)
		if w.Code != 404 {
			t.Errorf("%s: status %d", p, w.Code)
		}
		if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
			t.Errorf("%s: content type %q", p, ct)
		}
		if !strings.Contains(w.Body.String(), "Page not found") {
			t.Errorf("%s: body is not the 404 page:\n%s", p, w.Body.String())
		}
	}
}
