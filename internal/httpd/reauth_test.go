package httpd

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"gitbay.org/gitbay/internal/config"
	"gitbay.org/gitbay/internal/control"
	"gitbay.org/gitbay/internal/store"
)

// A session signed in longer ago than ReauthWindow cannot mint from the
// settings page: the form comes back with the refusal and a sign-in
// link, and the sign-in returns to /settings (#297).
func TestWebMintNeedsRecentSignIn(t *testing.T) {
	s, st, u := newTokenTestServer(t)
	stale := u
	stale.SignedInAt = time.Now().Add(-control.ReauthWindow - time.Minute)
	rr := submitAccountForm(t, s, stale, url.Values{"field": {"token-create"}, "name": {"laptop"}, "scope": {"full"}})
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("status %d, body %s", rr.Code, rr.Body.String())
	}
	if list, err := st.ListAPITokens(u.ID); err != nil || len(list) != 0 {
		t.Fatalf("a stale session minted %+v (%v)", list, err)
	}

	req := httptest.NewRequest("GET", "/settings", nil)
	for _, c := range rr.Result().Cookies() {
		req.AddCookie(c)
	}
	page := httptest.NewRecorder()
	s.accountPage(page, req, stale)
	body := page.Body.String()
	if !strings.Contains(body, control.ReauthRefusal) {
		t.Fatalf("refusal not shown: %s", body)
	}
	if !strings.Contains(body, `<a href="/login">Sign in again</a>`) {
		t.Fatalf("no sign-in link: %s", body)
	}
	var next string
	for _, c := range page.Result().Cookies() {
		if c.Name == nextCookie {
			next = c.Value
		}
	}
	if next != url.QueryEscape("/settings") {
		t.Fatalf("gitbay_next = %q, want /settings", next)
	}
}

// An API token has no browser session: minting through the API is not
// held to the sign-in window.
func TestAPIMintIgnoresTheSignInWindow(t *testing.T) {
	s, st, u := newTokenTestServer(t)
	if err := st.CreateAPIToken(u.ID, "ci", store.HashToken("gb_reauthtest"), "full", nil, 0); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", "/api/v1/cmd",
		strings.NewReader(`{"argv":["token","create","--name","second","--scope","read"]}`))
	req.Header.Set("Authorization", "Bearer gb_reauthtest")
	rr := httptest.NewRecorder()
	s.apiCmd(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rr.Code, rr.Body.String())
	}
}

// A fresh session mints without any refusal.
func TestWebMintFreshSessionSucceeds(t *testing.T) {
	s, st, u := newTokenTestServer(t)
	rr := submitAccountForm(t, s, u, url.Values{"field": {"token-create"}, "name": {"laptop"}, "scope": {"full"}})
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rr.Code, rr.Body.String())
	}
	if list, err := st.ListAPITokens(u.ID); err != nil || len(list) != 1 {
		t.Fatalf("token not minted: %+v (%v)", list, err)
	}
}

// A stale session posting a grant form (org members add, on the
// organization's people page) also sees the refusal and the sign-in
// link, and the membership is not created.
func TestWebGrantNeedsRecentSignIn(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.MigrateUp(); err != nil {
		t.Fatal(err)
	}
	uid, err := st.CreateUser("alice", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateUser("bob", false); err != nil {
		t.Fatal(err)
	}
	fresh := store.User{ID: uid, Username: "alice", SignedInAt: time.Now()}
	stale := fresh
	stale.SignedInAt = time.Now().Add(-control.ReauthWindow - time.Minute)

	cfg := config.Default()
	cfg.Web.Mode = "accounts"
	s := New(cfg, st, nil)
	if _, msg, ok := s.runControl(fresh, []string{"org", "create", "krz"}); !ok {
		t.Fatalf("org create: %s", msg)
	}

	req := httptest.NewRequest("POST", "/krz",
		strings.NewReader(url.Values{"field": {"member-add"}, "user": {"bob"}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetPathValue("owner", "krz")
	rr := httptest.NewRecorder()
	s.orgSubmit(rr, req, stale)
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("status %d, body %s", rr.Code, rr.Body.String())
	}

	req2 := httptest.NewRequest("GET", "/krz/-/people", nil)
	req2.SetPathValue("owner", "krz")
	for _, c := range rr.Result().Cookies() {
		req2.AddCookie(c)
	}
	req2.AddCookie(sessionCookieFor(t, s, st, uid))
	page := httptest.NewRecorder()
	s.ownerProfile(page, req2)
	body := page.Body.String()
	if !strings.Contains(body, control.ReauthRefusal) {
		t.Fatalf("refusal not shown: %s", body)
	}
	if !strings.Contains(body, `<a href="/login">Sign in again</a>`) {
		t.Fatalf("no sign-in link: %s", body)
	}
	var next string
	for _, c := range page.Result().Cookies() {
		if c.Name == nextCookie {
			next = c.Value
		}
	}
	if next != url.QueryEscape("/krz/-/people") {
		t.Fatalf("gitbay_next = %q, want /krz/-/people", next)
	}

	org, err := st.OrgByName("krz")
	if err != nil {
		t.Fatal(err)
	}
	members, _ := st.OrgMembers(org.ID)
	for _, m := range members {
		if m.Username == "bob" {
			t.Fatalf("a stale session added bob to the org")
		}
	}
}
