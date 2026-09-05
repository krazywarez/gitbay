package httpd

import (
	"net/http"
	"testing"

	"gitbay.org/gitbay/internal/config"
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
