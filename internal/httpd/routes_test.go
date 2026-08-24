package httpd

import (
	"strings"
	"testing"

	"gitbay.org/gitbay/internal/config"
	"gitbay.org/gitbay/internal/policy"
)

// TestViewOnlyHasNoMutatingRoutes is the structural guarantee from the plan:
// under web.mode = "view_only" the route table must contain no mutating
// route — not hidden ones, none at all.
func TestViewOnlyHasNoMutatingRoutes(t *testing.T) {
	cfg := config.Default()
	cfg.Web.Mode = "view_only"
	s := New(cfg, nil)

	for _, r := range s.Routes() {
		if r.Mutating {
			t.Errorf("view_only route table contains mutating route %s %s", r.Method, r.Pattern)
		}
		// The only POSTs allowed are the git transport endpoints: a pure
		// read (upload-pack) and a static refusal (receive-pack).
		if r.Method != "GET" && !strings.Contains(r.Pattern, "git-upload-pack") && !strings.Contains(r.Pattern, "git-receive-pack") {
			t.Errorf("view_only route table contains non-GET route %s %s", r.Method, r.Pattern)
		}
		for _, word := range []string{"login", "logout", "register", "edit", "new", "settings"} {
			if strings.Contains(r.Pattern, "/"+word) {
				t.Errorf("view_only route table contains account-mode pattern %s %s", r.Method, r.Pattern)
			}
		}
	}
}

// TestAPIRouteGating: the API route exists only when [api] enabled = true.
func TestAPIRouteGating(t *testing.T) {
	has := func(cfg config.Config) bool {
		for _, r := range New(cfg, nil).Routes() {
			if r.Pattern == "/api/v1/cmd" {
				return true
			}
		}
		return false
	}
	if has(config.Default()) {
		t.Fatal("API route present with api disabled (the default)")
	}
	cfg := config.Default()
	cfg.API.Enabled = true
	if !has(cfg) {
		t.Fatal("API route missing with api enabled")
	}
}

// TestAccountsModeHasLoginRoute is the positive counterpart: switching the
// mode on registers the session routes.
func TestAccountsModeHasLoginRoute(t *testing.T) {
	cfg := config.Default()
	cfg.Web.Mode = "accounts"
	s := New(cfg, nil)
	found := false
	for _, r := range s.Routes() {
		if r.Pattern == "/login" {
			found = true
		}
	}
	if !found {
		t.Fatal("accounts mode is missing the /login route")
	}
}

// TestTopLevelRouteWordsAreReserved keeps the route table and the reserved
// username list in agreement: every literal first path segment must be an
// unclaimable username.
func TestTopLevelRouteWordsAreReserved(t *testing.T) {
	cfg := config.Default()
	cfg.Web.Mode = "accounts" // superset of routes
	s := New(cfg, nil)
	for _, r := range s.Routes() {
		seg := strings.TrimPrefix(r.Pattern, "/")
		seg, _, _ = strings.Cut(seg, "/")
		if seg == "" || strings.HasPrefix(seg, "{") {
			continue // wildcard or root
		}
		if !policy.Reserved(seg) {
			t.Errorf("top-level route word %q is not in the reserved username list", seg)
		}
	}
}
