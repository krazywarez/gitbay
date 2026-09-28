package httpd

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"gitbay.org/gitbay/internal/config"
	"gitbay.org/gitbay/internal/store"
)

// A repository's MR list offers the right next step by access level: a
// writer gets "New merge request", a signed-in reader without push gets
// a fork link, and a signed-out visitor gets a sign-in prompt (#270).
func TestMRsListContributionHintByAccess(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.MigrateUp(); err != nil {
		t.Fatal(err)
	}
	owner, err := st.CreateUser("alice", false)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := st.CreateUser("bob", false)
	if err != nil {
		t.Fatal(err)
	}
	forker, err := st.CreateUser("carol", false)
	if err != nil {
		t.Fatal(err)
	}
	repoID, err := st.CreateRepo("user", owner, "app", "public")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateFork("user", forker, "app", "public", repoID); err != nil {
		t.Fatal(err)
	}

	cfg := config.Default()
	cfg.Web.Mode = "accounts"
	s := New(cfg, st, nil)

	// mrs reads the viewer through s.viewer(r), which resolves a
	// session cookie (internal/httpd/accounts.go:37-47) rather than
	// taking the viewer as a parameter the way a POST handler test
	// does. Give a real viewer a real session; leave the request
	// cookie-less for the anonymous case.
	sessionFor := func(uid int64) *http.Cookie {
		tok, hash, err := store.NewToken()
		if err != nil {
			t.Fatal(err)
		}
		if err := st.CreateWebSession(hash, uid, time.Hour); err != nil {
			t.Fatal(err)
		}
		return s.sessionCookieFor(tok)
	}

	get := func(uid int64) string {
		req := httptest.NewRequest("GET", "/alice/app/mrs", nil)
		req.SetPathValue("owner", "alice")
		req.SetPathValue("repo", "app")
		if uid != 0 {
			req.AddCookie(sessionFor(uid))
		}
		rr := httptest.NewRecorder()
		s.mrs(rr, req)
		return rr.Body.String()
	}
	anonymous := get(0)
	if !strings.Contains(anonymous, "Sign in to propose a change") {
		t.Errorf("signed-out visitor: missing sign-in prompt:\n%s", anonymous)
	}
	if strings.Contains(anonymous, "New merge request") {
		t.Error("signed-out visitor should not see New merge request")
	}

	readerOut := get(reader)
	if !strings.Contains(readerOut, "Fork this repository to propose a change") {
		t.Errorf("reader without push: missing fork hint:\n%s", readerOut)
	}

	ownerOut := get(owner)
	if !strings.Contains(ownerOut, "New merge request") {
		t.Errorf("owner: missing New merge request link:\n%s", ownerOut)
	}

	forkerOut := get(forker)
	if !strings.Contains(forkerOut, "New merge request") {
		t.Errorf("reader with a writable fork: missing New merge request link:\n%s", forkerOut)
	}
	if strings.Contains(forkerOut, "Fork this repository to propose a change") {
		t.Error("reader with a writable fork should not see the fork hint")
	}
}
