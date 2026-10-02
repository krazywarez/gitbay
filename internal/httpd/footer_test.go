package httpd

import (
	"strings"
	"testing"
)

// The footer links the operator's terms and abuse pages only when
// configured, on a page with no viewer and on the 404 page.
func TestFooterOperatorLinks(t *testing.T) {
	s, _, _ := newTokenTestServer(t)
	for _, path := range []string{"/privacy", "/no-such-owner"} {
		if body := get(t, s.Handler(), path, nil).Body.String(); strings.Contains(body, "Report abuse") || strings.Contains(body, ">Terms<") {
			t.Errorf("%s: footer links with nothing configured", path)
		}
	}
	s.cfg.Web.AbuseURL = "https://example.test/wiki/Abuse"
	s.cfg.Web.TermsURL = "https://example.test/wiki/Terms"
	for _, path := range []string{"/privacy", "/no-such-owner"} {
		body := get(t, s.Handler(), path, nil).Body.String()
		if !strings.Contains(body, `<a href="https://example.test/wiki/Abuse">Report abuse</a>`) ||
			!strings.Contains(body, `<a href="https://example.test/wiki/Terms">Terms</a>`) {
			t.Errorf("%s: footer lacks the configured links", path)
		}
	}
}
