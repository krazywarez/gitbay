package httpd

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"gitbay.org/gitbay/internal/config"
)

// TestMutatingRoutesRequireCheckOrigin pins the invariant checkOrigin's doc
// comment rests on (#155, #157): the session cookie is SameSite=Lax, which
// withholds it from a cross-site POST but not a cross-site top-level GET, so
// checkOrigin has to be the thing that refuses every other mutating route.
// checkOrigin is the outermost wrapper, so a cross-site Origin is refused
// with 403 before any handler logic runs — no session or repository needed.
//
// Two routes are exempt on purpose, not by omission:
var checkOriginAllowlist = map[string]string{
	// Authenticates only via "Authorization: Bearer ...". A cross-site
	// browser request cannot attach one, so there is no cookie for
	// checkOrigin to protect here.
	"POST /api/v1/cmd": "bearer-token auth, no cookie in play",
	// Consumes a single-use token from the query string and sets no
	// cookie on failure; browsers withhold Origin from a cross-site
	// top-level GET, which is the only way this route is ever reached
	// cross-site.
	"GET /login": "single-use token GET, no cookie read",
}

func TestMutatingRoutesRequireCheckOrigin(t *testing.T) {
	cfg := config.Default()
	cfg.Web.Mode = "accounts" // superset of routes
	cfg.API.Enabled = true
	s := New(cfg, nil)

	for _, r := range s.Routes() {
		if !r.Mutating {
			continue
		}
		key := r.Method + " " + r.Pattern
		if _, exempt := checkOriginAllowlist[key]; exempt {
			continue
		}
		req := httptest.NewRequest(r.Method, "http://example.com/", nil)
		req.Header.Set("Origin", "https://evil.example")
		rr := httptest.NewRecorder()
		r.Handler(rr, req)
		if rr.Code != http.StatusForbidden {
			t.Errorf("%s: cross-site Origin got status %d, want %d (missing checkOrigin?)",
				key, rr.Code, http.StatusForbidden)
		}
	}
}
