package httpd

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gitbay.org/gitbay/internal/control"
	"gitbay.org/gitbay/internal/gitutil"
	"gitbay.org/gitbay/internal/store"
)

// repo download turned away by a full pack limit is 503 with
// Retry-After on both API endpoints, not a 500.
func TestAPIBusyDownloadIs503(t *testing.T) {
	s := busyServer(t)
	s.apiLimit = newAPILimiter(0)
	if err := gitutil.InitBare(control.RepoDir(s.cfg.Server.Root, "alice", "app"), "main", t.TempDir()); err != nil {
		t.Fatal(err)
	}
	seed(t, s, 10)
	alice, err := s.st.UserByUsername("alice")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.st.CreateAPIToken(alice.ID, "t", store.HashToken("secret"), "full", nil, 0); err != nil {
		t.Fatal(err)
	}
	check := func(what string, w *httptest.ResponseRecorder) {
		t.Helper()
		if w.Code != http.StatusServiceUnavailable || w.Header().Get("Retry-After") != "60" ||
			!strings.Contains(w.Body.String(), "busy") {
			t.Fatalf("%s: status %d, Retry-After %q, body %s", what, w.Code, w.Header().Get("Retry-After"), w.Body.String())
		}
		if w.Header().Get("ETag") != "" {
			t.Fatalf("%s: a busy answer carries an ETag", what)
		}
	}

	r := httptest.NewRequest("POST", "/api/v1/cmd", strings.NewReader(`{"argv":["repo","download","alice/app"]}`))
	r.Header.Set("Authorization", "Bearer secret")
	w := httptest.NewRecorder()
	s.apiCmd(w, r)
	check("POST", w)

	r = httptest.NewRequest("GET", "/api/v1/read?argv=repo&argv=download&argv=alice/app", nil)
	r.Header.Set("Authorization", "Bearer secret")
	w = httptest.NewRecorder()
	s.apiRead(w, r)
	check("GET", w)
}
