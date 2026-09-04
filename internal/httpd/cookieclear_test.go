package httpd

import (
	"net/http"
	"testing"

	"gitbay.org/gitbay/internal/config"
)

// A cookie that clears a session should carry the attributes the one that
// set it carried. Deletion works without them, so this is consistency —
// but a reviewer comparing the two paths should not have to work out
// whether the difference is deliberate (go:S2092, go:S3330, #153).
func TestClearCookieMirrorsTheSettingCall(t *testing.T) {
	for _, tls := range []string{"acme", "off"} {
		s := &Server{cfg: config.Config{}}
		s.cfg.HTTP.TLS = tls
		c := s.clearCookie(sessionCookie, http.SameSiteStrictMode)

		if c.Value != "" || c.MaxAge >= 0 {
			t.Errorf("tls=%s: not an expiring cookie: value=%q maxage=%d", tls, c.Value, c.MaxAge)
		}
		if !c.HttpOnly {
			t.Errorf("tls=%s: clearing cookie is not HttpOnly", tls)
		}
		if c.SameSite != http.SameSiteStrictMode {
			t.Errorf("tls=%s: SameSite = %v, want Strict", tls, c.SameSite)
		}
		if c.Path != "/" {
			t.Errorf("tls=%s: Path = %q, want /", tls, c.Path)
		}
		// Secure follows TLS exactly as the setting calls do: forcing it
		// on would make the cookie undeletable over plain HTTP, which is
		// a supported deployment.
		if want := tls != "off"; c.Secure != want {
			t.Errorf("tls=%s: Secure = %v, want %v", tls, c.Secure, want)
		}
	}
}
