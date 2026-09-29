package httpd

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gitbay.org/gitbay/internal/config"
	"gitbay.org/gitbay/internal/store"
)

// A pinned query shows on the dashboard and its page lists what it
// matches, one page at a time, never another user's private rows.
func TestSavedQueryPages(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.MigrateUp(); err != nil {
		t.Fatal(err)
	}
	alice, err := st.CreateUser("alice", false)
	if err != nil {
		t.Fatal(err)
	}
	bob, err := st.CreateUser("bob", false)
	if err != nil {
		t.Fatal(err)
	}
	pub, err := st.CreateRepo("user", alice, "pub", "public")
	if err != nil {
		t.Fatal(err)
	}
	secret, err := st.CreateRepo("user", bob, "secret", "private")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < queryPerPage+1; i++ {
		if _, err := st.CreateIssue(pub, alice, "public issue", "", "md"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.CreateIssue(secret, bob, "bob's secret", "", "md"); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveQuery(alice, "open", "is:open", false); err != nil {
		t.Fatal(err)
	}
	if err := st.PinSavedQuery(alice, "open", true); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Web.Mode = "accounts"
	s := New(cfg, st, nil)
	viewer := store.User{ID: alice, Username: "alice"}

	get := func(path string) *httptest.ResponseRecorder {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest("GET", path, nil)
		if owner, rest, ok := strings.Cut(strings.TrimPrefix(req.URL.Path, "/"), "/-/queries"); ok {
			req.SetPathValue("owner", owner)
			req.SetPathValue("name", strings.TrimPrefix(rest, "/"))
		}
		if req.URL.Path == "/" {
			s.dashboard(rr, req, viewer)
		} else {
			s.queriesPage(rr, req, viewer)
		}
		return rr
	}

	dash := get("/").Body.String()
	for _, want := range []string{
		`<a href="/alice/-/queries/open">open</a> <span class="count">51</span>`,
		`<code>is:open</code>`,
		`<a href="/alice/-/queries/open">all 51 →</a>`,
	} {
		if !strings.Contains(dash, want) {
			t.Errorf("dashboard lacks %q", want)
		}
	}

	first := get("/alice/-/queries/open")
	if first.Code != http.StatusOK {
		t.Fatalf("query page: %d %s", first.Code, first.Body.String())
	}
	body := first.Body.String()
	if n := strings.Count(body, `<span class="repo">alice/pub#`); n != queryPerPage {
		t.Errorf("first page lists %d rows, want %d", n, queryPerPage)
	}
	if strings.Contains(body, "secret") || strings.Contains(dash, "secret") {
		t.Error("a private repository of another user reached the page")
	}
	i := strings.Index(body, `<p class="pager"><a href="?cursor=`)
	if i < 0 {
		t.Fatalf("no next link:\n%s", body)
	}
	href := body[i+len(`<p class="pager"><a href="`):]
	href = strings.ReplaceAll(href[:strings.Index(href, `"`)], "&amp;", "&")
	second := get("/alice/-/queries/open" + href).Body.String()
	if n := strings.Count(second, `<span class="repo">alice/pub#`); n != 1 || !strings.Contains(second, `alice/pub#1<`) {
		t.Errorf("second page lists %d rows, want the oldest one", n)
	}

	if rr := get("/alice/-/queries/nosuch"); rr.Code != http.StatusNotFound {
		t.Errorf("unknown query: %d, want 404", rr.Code)
	}
	if rr := get("/bob/-/queries/open"); rr.Code != http.StatusNotFound {
		t.Errorf("alice under bob's name: %d, want 404", rr.Code)
	}
	list := get("/alice/-/queries").Body.String()
	if !strings.Contains(list, `<a href="/alice/-/queries/open">open</a> <span class="chip">pinned</span>`) {
		t.Errorf("query list lacks the pinned query:\n%s", list)
	}
}
