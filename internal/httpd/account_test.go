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

	s := New(config.Default(), st, nil)
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
	s := New(config.Default(), st, nil)

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
	s := New(config.Default(), st, nil)

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

	s := New(config.Default(), st, nil)
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

// assertAudited fails the test unless an audit row with the given action
// prefix exists — proof a handler dispatched through the control
// registry rather than writing the store directly, since only Dispatch
// itself calls Store.Audit.
func assertAudited(t *testing.T, st *store.Store, prefix string) {
	t.Helper()
	entries, err := st.AuditEntries(store.AuditFilter{ActionPrefix: prefix, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) == 0 {
		t.Fatalf("no audit row with action prefix %q", prefix)
	}
}

// Pinning writes through the repo pin command, not the store directly,
// so it carries the same audit trail and write budget as every other
// mutating command (#261).
func TestPinToggleDispatchesRepoPin(t *testing.T) {
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
	if _, err := st.CreateRepo("user", uid, "app", "public"); err != nil {
		t.Fatal(err)
	}

	s := New(config.Default(), st, nil)
	req := httptest.NewRequest("POST", "/alice/app/pin", nil)
	req.SetPathValue("owner", "alice")
	req.SetPathValue("repo", "app")
	rr := httptest.NewRecorder()
	s.pinToggle(rr, req, u)

	repo, err := st.RepoByPath("alice/app")
	if err != nil {
		t.Fatal(err)
	}
	if !st.IsPinned(uid, repo.ID) {
		t.Fatal("pin did not take effect")
	}
	assertAudited(t, st, "cmd repo pin")

	rr2 := httptest.NewRecorder()
	s.pinToggle(rr2, req, u)
	if st.IsPinned(uid, repo.ID) {
		t.Fatal("second toggle should have unpinned")
	}
	assertAudited(t, st, "cmd repo unpin")
}

// The watch button cycles default, watching, muted — the three states
// repo watch/repo mute/repo unwatch already support — rather than the
// two the store-writing version offered (#261, #271).
func TestWatchToggleCyclesThroughMuted(t *testing.T) {
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
	if _, err := st.CreateRepo("user", uid, "app", "public"); err != nil {
		t.Fatal(err)
	}
	repo, err := st.RepoByPath("alice/app")
	if err != nil {
		t.Fatal(err)
	}

	s := New(config.Default(), st, nil)
	req := httptest.NewRequest("POST", "/alice/app/watch", nil)
	req.SetPathValue("owner", "alice")
	req.SetPathValue("repo", "app")

	click := func() string {
		rr := httptest.NewRecorder()
		s.watchToggle(rr, req, u)
		return st.RepoWatchState(repo.ID, uid)
	}
	if got := click(); got != "watching" {
		t.Fatalf("first click: got %q, want watching", got)
	}
	assertAudited(t, st, "cmd repo watch")
	if got := click(); got != "muted" {
		t.Fatalf("second click: got %q, want muted", got)
	}
	assertAudited(t, st, "cmd repo mute")
	if got := click(); got != "" {
		t.Fatalf("third click: got %q, want default (unwatched)", got)
	}
	assertAudited(t, st, "cmd repo unwatch")
}
