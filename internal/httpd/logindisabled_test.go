package httpd

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"gitbay.org/gitbay/internal/config"
	"gitbay.org/gitbay/internal/store"
)

// login() re-reads the user after ConsumeLoginToken and refuses a disabled
// account with the same badLoginToken page a bad token gets (#155, #156).
// SetUserDisabled also deletes login_tokens, which would mask this guard if
// the test went through it, so the token is inserted directly and the row
// is disabled with a bare UPDATE — bypassing SetUserDisabled entirely.
func TestLoginRefusesTokenForDisabledAccount(t *testing.T) {
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
	tok, hash, err := store.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CreateLoginToken(uid, hash, time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB.Exec("UPDATE users SET disabled = 1 WHERE id = ?", uid); err != nil {
		t.Fatal(err)
	}

	s := New(config.Default(), st)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/login?token="+tok, nil)
	s.login(rr, req)

	for _, c := range rr.Result().Cookies() {
		if c.Name == sessionCookie {
			t.Fatalf("login set a session cookie for a disabled account: %+v", c)
		}
	}
	if !strings.Contains(rr.Body.String(), badLoginToken) {
		t.Errorf("body = %q, want the bad-token page", rr.Body.String())
	}
}
