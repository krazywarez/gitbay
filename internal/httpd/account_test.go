package httpd

import (
	"net/http/httptest"
	"strings"
	"testing"

	"gitbay.org/gitbay/internal/config"
	"gitbay.org/gitbay/internal/store"
)

// The settings page carries a push toggle beside the mail and watch ones,
// and lists registered devices by label and truncated token. The full
// token is device-identifying and must never reach the page.
func TestAccountPagePushToggleAndDevices(t *testing.T) {
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
	token := strings.Repeat("a", 64)
	if _, err := st.AddPushDevice(uid, token, "iphone"); err != nil {
		t.Fatal(err)
	}

	s := New(config.Default(), st)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/settings", nil)
	s.accountPage(rr, req, store.User{ID: uid, Username: "alice"})

	body := rr.Body.String()
	if !strings.Contains(body, `value="notify-push"`) {
		t.Fatal("no push toggle")
	}
	if !strings.Contains(body, "iphone") {
		t.Fatal("the device is not listed")
	}
	// A token is device-identifying and is never printed in full.
	if strings.Contains(body, token) {
		t.Fatal("the page printed a device token in full")
	}
}
