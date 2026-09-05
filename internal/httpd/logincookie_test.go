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
func TestSessionCookieIsLax(t *testing.T) {
	s := &Server{cfg: config.Config{}}
	s.cfg.HTTP.TLS = "acme"
	if got := s.clearCookie(sessionCookie, sessionSameSite); got.SameSite != http.SameSiteLaxMode {
		t.Errorf("clearing cookie SameSite = %v, want Lax", got.SameSite)
	}
	if sessionSameSite != http.SameSiteLaxMode {
		t.Errorf("sessionSameSite = %v, want Lax", sessionSameSite)
	}
}
