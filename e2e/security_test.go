package e2e

import (
	"net/http"
	"strings"
	"testing"
)

func TestSecurityHeaders(t *testing.T) {
	inst := startInstance(t)
	aliceKey := inst.newKey(t, "alice")
	inst.admin(t, "admin", "user", "create", "alice", "--key", aliceKey+".pub")
	inst.ssh(t, aliceKey, "", "repo", "create", "alice/app")

	resp, err := http.Get(inst.base() + "/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	h := resp.Header
	csp := h.Get("Content-Security-Policy")
	for _, want := range []string{"script-src 'none'", "frame-ancestors 'none'", "object-src 'none'"} {
		if !strings.Contains(csp, want) {
			t.Errorf("CSP missing %q: %s", want, csp)
		}
	}
	if h.Get("X-Frame-Options") != "DENY" {
		t.Errorf("X-Frame-Options = %q", h.Get("X-Frame-Options"))
	}
	if h.Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("nosniff missing")
	}
	if h.Get("Referrer-Policy") != "no-referrer" {
		t.Errorf("Referrer-Policy = %q", h.Get("Referrer-Policy"))
	}
	// TLS is off in tests, so HSTS must NOT be set (it would poison
	// plain-HTTP clients).
	if h.Get("Strict-Transport-Security") != "" {
		t.Errorf("HSTS set without TLS")
	}
	// Headers are present on repo pages too, not just the root.
	resp2, _ := http.Get(inst.base() + "/alice/app")
	resp2.Body.Close()
	if resp2.Header.Get("Content-Security-Policy") == "" {
		t.Error("CSP missing on repo page")
	}
}
