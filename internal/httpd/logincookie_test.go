package httpd

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"gitbay.org/gitbay/internal/config"
	"gitbay.org/gitbay/internal/store"
)

// The session cookie must be Lax, not Strict. A login link clicked in a mail
// client is a cross-site top-level navigation, and Strict can withhold the
// cookie through the redirect that follows, so the visitor lands logged out
// (#155). Cross-site POSTs stay protected: Lax withholds the cookie from them,
// and checkOrigin refuses them besides.
//
// The other two attributes are what keep the token out of a script's reach
// and off the wire in clear, so they are asserted on the same literal login
// hands to http.SetCookie.
func TestSessionCookieAttributes(t *testing.T) {
	if sessionSameSite != http.SameSiteLaxMode {
		t.Errorf("sessionSameSite = %v, want Lax", sessionSameSite)
	}
	for _, tls := range []string{"acme", "off"} {
		s := &Server{cfg: config.Config{}}
		s.cfg.HTTP.TLS = tls
		c := s.sessionCookieFor("tok")

		if c.SameSite != http.SameSiteLaxMode {
			t.Errorf("tls=%s: SameSite = %v, want Lax", tls, c.SameSite)
		}
		if !c.HttpOnly {
			t.Errorf("tls=%s: session cookie is not HttpOnly", tls)
		}
		if want := tls != "off"; c.Secure != want {
			t.Errorf("tls=%s: Secure = %v, want %v", tls, c.Secure, want)
		}
	}
}

// The login link's token rides in the query string — the one
// documented exception to "never in a URL" — so the response that
// consumes it must never be cached by an intermediary that might log
// or replay the URL (#261).
func TestLoginNoStoreHeader(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.MigrateUp(); err != nil {
		t.Fatal(err)
	}
	s := New(config.Default(), st)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/login?token=bogus", nil)
	s.login(rr, req)
	if got := rr.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
}
