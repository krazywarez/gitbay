package httpd

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"gitbay.org/gitbay/internal/config"
	"gitbay.org/gitbay/internal/store"
)

func reactServer(t *testing.T) (*Server, *store.Store, store.User, store.User) {
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
	rid, err := st.CreateRepo("user", aid, "app", "public")
	if err != nil {
		t.Fatal(err)
	}
	n, _ := st.CreateIssue(rid, aid, "bug", "body", "md")
	iss, _ := st.IssueByNumber(rid, n)
	st.AddIssueComment(iss.ID, aid, "a comment", "md")
	cfg := config.Default()
	cfg.Web.Mode = "accounts"
	return New(cfg, st, nil), st, store.User{ID: aid, Username: "alice"}, store.User{ID: bid, Username: "bob"}
}

func issueReq(method, target string, form url.Values) *http.Request {
	var r *http.Request
	if form != nil {
		r = httptest.NewRequest(method, target, strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	} else {
		r = httptest.NewRequest(method, target, nil)
	}
	r.SetPathValue("owner", "alice")
	r.SetPathValue("repo", "app")
	r.SetPathValue("n", "1")
	return r
}

func TestIssueReactionsOnPage(t *testing.T) {
	s, st, _, bob := reactServer(t)

	// Bob reacts to the body and to the comment through the form.
	for _, form := range []url.Values{
		{"add": {"+1"}},
		{"add": {"rocket"}, "comment": {"1"}},
	} {
		rr := httptest.NewRecorder()
		s.issueReactSubmit(rr, issueReq("POST", "/alice/app/issues/1/react", form), bob)
		if rr.Code != http.StatusSeeOther {
			t.Fatalf("%v: status %d: %s", form, rr.Code, rr.Body)
		}
	}

	// Signed in: a button for every reaction, his own marked pressed.
	req := issueReq("GET", "/alice/app/issues/1", nil)
	req.AddCookie(sessionCookieFor(t, s, st, bob.ID))
	rr := httptest.NewRecorder()
	s.issue(rr, req)
	body := rr.Body.String()
	if n := strings.Count(body, `class="react"`)+strings.Count(body, `class="react mine"`); n != 16 {
		t.Errorf("%d reaction buttons, want 16 (8 on the body, 8 on the comment)", n)
	}
	if !strings.Contains(body, `name="remove" value="&#43;1" class="react mine" aria-pressed="true"`) {
		t.Errorf("own reaction not marked:\n%s", body)
	}
	if !strings.Contains(body, `name="add" value="eyes"`) || !strings.Contains(body, `<input type="hidden" name="comment" value="1">`) {
		t.Error("add buttons or comment field missing")
	}

	// Signed out: counts only, no forms or buttons.
	rr = httptest.NewRecorder()
	s.issue(rr, issueReq("GET", "/alice/app/issues/1", nil))
	body = rr.Body.String()
	if strings.Contains(body, `class="react"`) && strings.Contains(body, "<button type=\"submit\" name=\"add\"") {
		t.Error("buttons shown to a signed-out viewer")
	}
	if strings.Contains(body, `/react"`) {
		t.Error("react form shown to a signed-out viewer")
	}
	if n := strings.Count(body, `<span class="react"`); n != 2 {
		t.Errorf("%d counts, want 2:\n%s", n, body)
	}
	if !strings.Contains(body, "👍 1") || !strings.Contains(body, "🚀 1") {
		t.Error("counts missing")
	}

	// Pressing a pressed button takes it back.
	rr = httptest.NewRecorder()
	s.issueReactSubmit(rr, issueReq("POST", "/alice/app/issues/1/react", url.Values{"remove": {"+1"}}), bob)
	if got, _ := st.ReactionCounts("issue", 1, bob.ID); len(got[0]) != 0 {
		t.Errorf("reaction not removed: %+v", got)
	}
}
