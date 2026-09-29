package httpd

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"gitbay.org/gitbay/internal/config"
	"gitbay.org/gitbay/internal/control"
	"gitbay.org/gitbay/internal/gitutil"
	"gitbay.org/gitbay/internal/store"
)

type settingsEnv struct {
	s     *Server
	st    *store.Store
	alice store.User
	bob   store.User
	repo  store.Repo
}

func newSettingsEnv(t *testing.T) *settingsEnv {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.MigrateUp(); err != nil {
		t.Fatal(err)
	}
	aid, _ := st.CreateUser("alice", false)
	bid, _ := st.CreateUser("bob", false)
	if _, err := st.CreateRepo("user", aid, "app", "public"); err != nil {
		t.Fatal(err)
	}
	repo, err := st.RepoByPath("alice/app")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.GrantAccess(repo.ID, bid, "read"); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Web.Mode = "accounts"
	cfg.Server.Root = t.TempDir()
	cfg.Webhooks.AllowLocal = true
	if err := gitutil.InitBare(control.RepoDir(cfg.Server.Root, "alice", "app"), "main", t.TempDir()); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	return &settingsEnv{
		s: New(cfg, st, nil), st: st, repo: repo,
		alice: store.User{ID: aid, Username: "alice", SignedInAt: now},
		bob:   store.User{ID: bid, Username: "bob", SignedInAt: now},
	}
}

func (e *settingsEnv) post(u store.User, form url.Values) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", "/alice/app/settings", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetPathValue("owner", "alice")
	req.SetPathValue("repo", "app")
	rr := httptest.NewRecorder()
	e.s.settingsSubmit(rr, req, u)
	return rr
}

func (e *settingsEnv) page(u store.User) *httptest.ResponseRecorder {
	req := httptest.NewRequest("GET", "/alice/app/settings", nil)
	req.SetPathValue("owner", "alice")
	req.SetPathValue("repo", "app")
	rr := httptest.NewRecorder()
	e.s.settingsForm(rr, req, u)
	return rr
}

func TestSettingsAccessGrantRevoke(t *testing.T) {
	e := newSettingsEnv(t)
	cid, _ := e.st.CreateUser("carol", false)
	rr := e.post(e.alice, url.Values{"field": {"access-grant"}, "user": {"carol"}, "role": {"write"}})
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("grant: %d %s", rr.Code, rr.Body.String())
	}
	if role, _ := e.st.AccessRole(e.repo.ID, cid); role != "write" {
		t.Fatalf("role %q", role)
	}
	body := e.page(e.alice).Body.String()
	for _, want := range []string{"carol", "direct", `value="access-revoke"`, "owner"} {
		if !strings.Contains(body, want) {
			t.Errorf("page lacks %q", want)
		}
	}
	rr = e.post(e.alice, url.Values{"field": {"access-revoke"}, "user": {"carol"}})
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("revoke: %d %s", rr.Code, rr.Body.String())
	}
	if role, _ := e.st.AccessRole(e.repo.ID, cid); role != "" {
		t.Fatalf("still has %q", role)
	}
}

func TestSettingsAccessRefusalShown(t *testing.T) {
	e := newSettingsEnv(t)
	rr := e.post(e.alice, url.Values{"field": {"access-grant"}, "user": {"nobody"}, "role": {"read"}})
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "no such user &#34;nobody&#34;") {
		t.Fatalf("%d %s", rr.Code, rr.Body.String())
	}
}

func TestSettingsWebhookSecretStaysOffThePage(t *testing.T) {
	e := newSettingsEnv(t)
	const secret = "s3cr3t-value-xyz"
	rr := e.post(e.alice, url.Values{"field": {"webhook-add"}, "url": {"http://127.0.0.1:9/hook"},
		"events": {"push"}, "secret": {secret}})
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("add: %d %s", rr.Code, rr.Body.String())
	}
	hooks, _ := e.st.ListWebhooks(e.repo.ID)
	if len(hooks) != 1 || hooks[0].Secret != secret || hooks[0].Events != "push" {
		t.Fatalf("stored %+v", hooks)
	}
	if strings.Contains(rr.Header().Get("Location"), secret) || strings.Contains(strings.Join(rr.Header().Values("Set-Cookie"), ";"), secret) {
		t.Fatal("secret in redirect or flash")
	}
	body := e.page(e.alice).Body.String()
	if strings.Contains(body, secret) || !strings.Contains(body, "signed") || !strings.Contains(body, "127.0.0.1:9/hook") {
		t.Fatalf("page: %s", body)
	}

	// A refused add re-renders the form without the secret.
	rr = e.post(e.alice, url.Values{"field": {"webhook-add"}, "url": {"ftp://x"}, "events": {"push"}, "secret": {secret}})
	if rr.Code != http.StatusOK || strings.Contains(rr.Body.String(), secret) {
		t.Fatalf("refusal: %d, secret echoed: %v", rr.Code, strings.Contains(rr.Body.String(), secret))
	}
	if !strings.Contains(rr.Body.String(), `role="alert"`) {
		t.Fatal("no error shown")
	}

	rr = e.post(e.alice, url.Values{"field": {"webhook-remove"}, "id": {"1"}})
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("remove: %d %s", rr.Code, rr.Body.String())
	}
	if hooks, _ := e.st.ListWebhooks(e.repo.ID); len(hooks) != 0 {
		t.Fatalf("still %+v", hooks)
	}
}

func TestSettingsWebhookRedeliver(t *testing.T) {
	e := newSettingsEnv(t)
	rr := e.post(e.alice, url.Values{"field": {"webhook-redeliver"}, "delivery": {"99"}})
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "no delivery 99") {
		t.Fatalf("%d %s", rr.Code, rr.Body.String())
	}
}

func TestSettingsRename(t *testing.T) {
	e := newSettingsEnv(t)
	rr := e.post(e.alice, url.Values{"field": {"rename"}, "name": {"Bad Name"}})
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), `role="alert"`) {
		t.Fatalf("refusal: %d", rr.Code)
	}
	rr = e.post(e.alice, url.Values{"field": {"rename"}, "name": {"tool"}})
	if rr.Code != http.StatusSeeOther || rr.Header().Get("Location") != "/alice/tool/settings" {
		t.Fatalf("%d %q", rr.Code, rr.Header().Get("Location"))
	}
	if _, err := os.Stat(control.RepoDir(e.s.cfg.Server.Root, "alice", "tool")); err != nil {
		t.Fatal(err)
	}
}

func TestSettingsDeleteNeedsTypedPath(t *testing.T) {
	e := newSettingsEnv(t)
	rr := e.post(e.alice, url.Values{"field": {"delete"}, "confirm": {"app"}})
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "type alice/app to confirm") {
		t.Fatalf("%d %s", rr.Code, rr.Body.String())
	}
	if _, err := e.st.RepoByPath("alice/app"); err != nil {
		t.Fatal("deleted without confirmation")
	}
	rr = e.post(e.alice, url.Values{"field": {"delete"}, "confirm": {"alice/app"}})
	if rr.Code != http.StatusSeeOther || rr.Header().Get("Location") != "/alice" {
		t.Fatalf("%d %q", rr.Code, rr.Header().Get("Location"))
	}
	if _, err := e.st.RepoByPath("alice/app"); err == nil {
		t.Fatal("not deleted")
	}
}

func TestSettingsTransfer(t *testing.T) {
	e := newSettingsEnv(t)
	if _, msg, ok := e.s.runControl(e.alice, []string{"org", "create", "krz"}); !ok {
		t.Fatal(msg)
	}
	rr := e.post(e.alice, url.Values{"field": {"transfer"}, "new-owner": {"krz"}})
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "type alice/app to confirm") {
		t.Fatalf("unconfirmed: %d", rr.Code)
	}
	rr = e.post(e.alice, url.Values{"field": {"transfer"}, "new-owner": {"nowhere"}, "confirm": {"alice/app"}})
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "cannot transfer to &#34;nowhere&#34;") {
		t.Fatalf("refusal: %d %s", rr.Code, rr.Body.String())
	}
	rr = e.post(e.alice, url.Values{"field": {"transfer"}, "new-owner": {"krz"}, "confirm": {"alice/app"}})
	if rr.Code != http.StatusSeeOther || rr.Header().Get("Location") != "/krz/app" {
		t.Fatalf("%d %q", rr.Code, rr.Header().Get("Location"))
	}
	if _, err := e.st.RepoByPath("krz/app"); err != nil {
		t.Fatal(err)
	}
}

// Only a repository admin reaches the page or the forms; a reader is
// refused before any command runs.
func TestSettingsNonAdminSeesNoForms(t *testing.T) {
	e := newSettingsEnv(t)
	if rr := e.page(e.bob); rr.Code != http.StatusForbidden || strings.Contains(rr.Body.String(), "webhook-add") {
		t.Fatalf("page: %d", rr.Code)
	}
	for _, form := range []url.Values{
		{"field": {"webhook-add"}, "url": {"http://127.0.0.1:9/h"}},
		{"field": {"access-grant"}, "user": {"bob"}, "role": {"admin"}},
		{"field": {"delete"}, "confirm": {"alice/app"}},
		{"field": {"rename"}, "name": {"x"}},
	} {
		if rr := e.post(e.bob, form); rr.Code != http.StatusForbidden {
			t.Errorf("%v: %d", form, rr.Code)
		}
	}
	if hooks, _ := e.st.ListWebhooks(e.repo.ID); len(hooks) != 0 {
		t.Fatal("a reader added a webhook")
	}
	if role, _ := e.st.AccessRole(e.repo.ID, e.bob.ID); role != "read" {
		t.Fatalf("role became %q", role)
	}
	if _, err := e.st.RepoByPath("alice/app"); err != nil {
		t.Fatal("a reader deleted it")
	}
	body := e.page(e.alice).Body.String()
	for _, want := range []string{`value="webhook-add"`, `value="access-grant"`, `value="rename"`, `value="transfer"`, `value="delete"`} {
		if !strings.Contains(body, want) {
			t.Errorf("admin page lacks %s", want)
		}
	}
}

func TestNewImportRefusalShown(t *testing.T) {
	e := newSettingsEnv(t)
	form := url.Values{"field": {"import"}, "owner": {"alice"}, "name": {"copy"},
		"from": {"https://user:pw@example.org/r.git"}, "token": {"tok-abc"}}
	req := httptest.NewRequest("POST", "/new", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	e.s.newSubmit(rr, req, e.alice)
	body := rr.Body.String()
	if rr.Code != http.StatusOK || !strings.Contains(body, "do not embed credentials in the URL") {
		t.Fatalf("%d %s", rr.Code, body)
	}
	if strings.Contains(body, "tok-abc") {
		t.Fatal("token echoed")
	}
	if !strings.Contains(body, `name="field" value="import"`) {
		t.Fatal("import form missing")
	}
}
