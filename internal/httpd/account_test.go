package httpd

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

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

// newTokenTestServer is a server over a fresh store with one user.
func newTokenTestServer(t *testing.T) (*Server, *store.Store, store.User) {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.MigrateUp(); err != nil {
		t.Fatal(err)
	}
	uid, err := st.CreateUser("alice", false)
	if err != nil {
		t.Fatal(err)
	}
	return New(config.Default(), st, nil), st, store.User{ID: uid, Username: "alice", SignedInAt: time.Now()}
}

// The settings page lists a user's API tokens with scope and expiry,
// never the hash (#264).
func TestAccountPageListsTokens(t *testing.T) {
	s, st, u := newTokenTestServer(t)
	if err := st.CreateAPIToken(u.ID, "laptop", "somehash", "read", nil, 0); err != nil {
		t.Fatal(err)
	}
	rr := httptest.NewRecorder()
	s.accountPage(rr, httptest.NewRequest("GET", "/settings", nil), u)
	body := rr.Body.String()
	if !strings.Contains(body, "<td>laptop</td>") || !strings.Contains(body, "<td>read</td>") {
		t.Fatalf("token row missing: %s", body)
	}
	if strings.Contains(body, "somehash") {
		t.Fatal("the page printed a token hash")
	}
}

// Creating a token answers the POST itself with the token, marked
// no-store, and puts it in no header: not a Location, not a cookie. A
// later GET of the page does not show it (#264).
func TestAccountSubmitTokenCreateShownOnce(t *testing.T) {
	s, st, u := newTokenTestServer(t)
	rr := submitAccountForm(t, s, u, url.Values{"field": {"token-create"}, "name": {"laptop"}, "scope": {"full"}})
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rr.Code, rr.Body.String())
	}
	if got := rr.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
	body := rr.Body.String()
	i := strings.Index(body, "gb_")
	if i < 0 {
		t.Fatalf("token not shown: %s", body)
	}
	token := body[i:]
	token = token[:strings.IndexAny(token, "<\n")]
	for name, vals := range rr.Header() {
		for _, v := range vals {
			if strings.Contains(v, token) {
				t.Errorf("header %s carries the token", name)
			}
		}
	}
	got, tk, err := st.APITokenUser(store.HashToken(token))
	if err != nil || got.ID != u.ID || tk.Name != "laptop" || tk.Scope != "full" {
		t.Fatalf("shown token does not resolve: %+v %+v %v", got, tk, err)
	}

	rr = httptest.NewRecorder()
	s.accountPage(rr, httptest.NewRequest("GET", "/settings", nil), u)
	if strings.Contains(rr.Body.String(), token) {
		t.Fatal("a later GET showed the token")
	}
	if !strings.Contains(rr.Body.String(), "<td>laptop</td>") {
		t.Fatal("the new token is not listed")
	}
}

// The form sends --scope explicitly, read unless full was picked, so the
// page does not depend on token create's own default (#264, #257).
func TestAccountSubmitTokenCreateScope(t *testing.T) {
	s, st, u := newTokenTestServer(t)
	for _, c := range []struct{ name, scope, want string }{
		{"a", "", "read"}, {"b", "read", "read"}, {"c", "bogus", "read"}, {"d", "full", "full"},
	} {
		rr := submitAccountForm(t, s, u, url.Values{"field": {"token-create"}, "name": {c.name}, "scope": {c.scope}})
		if rr.Code != http.StatusOK {
			t.Fatalf("%s: status %d", c.name, rr.Code)
		}
	}
	tokens, err := st.ListAPITokens(u.ID)
	if err != nil || len(tokens) != 4 {
		t.Fatalf("tokens: %v %v", tokens, err)
	}
	want := map[string]string{"a": "read", "b": "read", "c": "read", "d": "full"}
	for _, tk := range tokens {
		if tk.Scope != want[tk.Name] {
			t.Errorf("%s: scope %q, want %q", tk.Name, tk.Scope, want[tk.Name])
		}
	}
}

// A failed create redirects with the reason and shows no token.
func TestAccountSubmitTokenCreateRefusal(t *testing.T) {
	s, _, u := newTokenTestServer(t)
	for _, form := range []url.Values{
		{"field": {"token-create"}, "name": {""}},
		{"field": {"token-create"}, "name": {"x"}, "ttl": {"-1h"}},
	} {
		rr := submitAccountForm(t, s, u, form)
		if rr.Code != http.StatusSeeOther || strings.Contains(rr.Body.String(), "gb_") {
			t.Errorf("%v: status %d, body %s", form, rr.Code, rr.Body.String())
		}
	}
}

// Revoking a token requires the name typed back, the same guard every
// other removal on this page uses.
func TestAccountSubmitTokenRevokeRequiresConfirm(t *testing.T) {
	s, st, u := newTokenTestServer(t)
	if err := st.CreateAPIToken(u.ID, "laptop", "somehash", "read", nil, 0); err != nil {
		t.Fatal(err)
	}
	submitAccountForm(t, s, u, url.Values{"field": {"token-revoke"}, "name": {"laptop"}})
	if tokens, _ := st.ListAPITokens(u.ID); len(tokens) != 1 {
		t.Fatal("token revoked without confirmation")
	}
	rr := submitAccountForm(t, s, u, url.Values{"field": {"token-revoke"}, "name": {"laptop"}, "confirm": {"laptop"}})
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("status %d, body %s", rr.Code, rr.Body.String())
	}
	if tokens, _ := st.ListAPITokens(u.ID); len(tokens) != 0 {
		t.Fatal("token not revoked")
	}
}

// A cross-site POST to /settings is refused before a token is minted.
func TestAccountTokenCreateCrossSiteRefused(t *testing.T) {
	_, st, u := newTokenTestServer(t)
	cfg := config.Default()
	cfg.Web.Mode = "accounts"
	s := New(cfg, st, nil)
	form := url.Values{"field": {"token-create"}, "name": {"evil"}, "scope": {"full"}}
	for _, r := range s.Routes() {
		if r.Method != "POST" || r.Pattern != "/settings" {
			continue
		}
		req := httptest.NewRequest("POST", "http://example.com/settings", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Origin", "https://evil.example")
		rr := httptest.NewRecorder()
		r.Handler(rr, req)
		if rr.Code != http.StatusForbidden {
			t.Fatalf("status %d, want 403", rr.Code)
		}
		if tokens, _ := st.ListAPITokens(u.ID); len(tokens) != 0 {
			t.Fatal("a cross-site POST minted a token")
		}
		return
	}
	t.Fatal("no POST /settings route")
}

// A token named like a flag, which token create accepts, can still be
// revoked from the page: the name goes after "--".
func TestAccountSubmitTokenRevokeFlagLikeName(t *testing.T) {
	s, st, u := newTokenTestServer(t)
	rr := submitAccountForm(t, s, u, url.Values{"field": {"token-create"}, "name": {"--x"}})
	if rr.Code != http.StatusOK {
		t.Fatalf("create: status %d, body %s", rr.Code, rr.Body.String())
	}
	rr = submitAccountForm(t, s, u, url.Values{"field": {"token-revoke"}, "name": {"--x"}, "confirm": {"--x"}})
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("revoke: status %d", rr.Code)
	}
	if tokens, _ := st.ListAPITokens(u.ID); len(tokens) != 0 {
		t.Fatalf("token not revoked: %+v", tokens)
	}
}

// The audit row for a web token create records the command but not the
// minted token.
func TestAccountTokenCreateAuditOmitsToken(t *testing.T) {
	s, st, u := newTokenTestServer(t)
	rr := submitAccountForm(t, s, u, url.Values{"field": {"token-create"}, "name": {"laptop"}})
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "gb_") {
		t.Fatalf("create: status %d", rr.Code)
	}
	rows, err := st.DB.Query("SELECT action, data_json FROM audit_log")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	found := false
	for rows.Next() {
		var action, data string
		if err := rows.Scan(&action, &data); err != nil {
			t.Fatal(err)
		}
		if action == "cmd token create" {
			found = true
		}
		if strings.Contains(data, "gb_") {
			t.Errorf("audit row %q carries the token: %s", action, data)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("no cmd token create audit row")
	}
}
