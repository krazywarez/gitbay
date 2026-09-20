package httpd

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
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

// submit posts an account settings form as u and returns the recorder.
func submitAccountForm(t *testing.T, s *Server, u store.User, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", "/settings", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	s.accountSubmit(rr, req, u)
	return rr
}

// Posting notify-push dispatches to notifications settings push, the same
// path the mail and watch toggles already use.
func TestAccountSubmitNotifyPush(t *testing.T) {
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
	u := store.User{ID: uid, Username: "alice"}
	s := New(config.Default(), st)

	rr := submitAccountForm(t, s, u, url.Values{"field": {"notify-push"}, "push": {"on"}})
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("status %d, body %s", rr.Code, rr.Body.String())
	}
	if on, err := st.PushEnabled(uid); err != nil || !on {
		t.Fatalf("PushEnabled after notify-push=on: %v %v", on, err)
	}

	submitAccountForm(t, s, u, url.Values{"field": {"notify-push"}})
	if on, err := st.PushEnabled(uid); err != nil || on {
		t.Fatalf("PushEnabled after notify-push off: %v %v", on, err)
	}
}

// Removing a device requires the device id typed back, and then
// dispatches to notifications device remove, scoped to the caller's own
// account. The id is what the form dispatches on, so the guard is
// derived server-side the way key-remove derives its own.
func TestAccountSubmitDeviceRemove(t *testing.T) {
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
	u := store.User{ID: uid, Username: "alice"}
	token := strings.Repeat("b", 64)
	id, err := st.AddPushDevice(uid, token, "iphone")
	if err != nil {
		t.Fatal(err)
	}
	s := New(config.Default(), st)

	idStr := strconv.FormatInt(id, 10)

	// Without the typed confirmation, the device survives.
	submitAccountForm(t, s, u, url.Values{"field": {"device-remove"}, "id": {idStr}})
	if devices, _ := st.PushDevices(uid); len(devices) != 1 {
		t.Fatalf("device removed without confirmation: %v", devices)
	}

	rr := submitAccountForm(t, s, u, url.Values{"field": {"device-remove"}, "id": {idStr}, "confirm": {idStr}})
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("status %d, body %s", rr.Code, rr.Body.String())
	}
	if devices, _ := st.PushDevices(uid); len(devices) != 0 {
		t.Fatalf("device not removed: %v", devices)
	}
}

// A short token reaches no part of the page — not the visible column,
// and not a hidden input, aria-label or placeholder either. Device add
// enforces no minimum length, so a token this short is a value the store
// can hold, and it is device-identifying whatever its length.
func TestAccountPageMasksAShortDeviceToken(t *testing.T) {
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
	id, err := st.AddPushDevice(uid, "abc123", "iphone")
	if err != nil {
		t.Fatal(err)
	}

	s := New(config.Default(), st)
	rr := httptest.NewRecorder()
	s.accountPage(rr, httptest.NewRequest("GET", "/settings", nil), store.User{ID: uid, Username: "alice"})

	body := rr.Body.String()
	if strings.Contains(body, "abc123") {
		t.Fatalf("the short token reached the page:\n%s", body)
	}
	// What the removal asks for has to be on screen to be typed back.
	idStr := strconv.FormatInt(id, 10)
	if !strings.Contains(body, `aria-label="Type `+idStr+` to confirm"`) {
		t.Fatalf("removal does not confirm on the device id:\n%s", body)
	}
	if !strings.Contains(body, `<th scope="col">id</th>`) {
		t.Fatalf("the device table has no id column:\n%s", body)
	}
}
