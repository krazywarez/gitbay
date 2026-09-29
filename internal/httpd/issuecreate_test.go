package httpd

import (
	"html/template"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"gitbay.org/gitbay/internal/config"
	"gitbay.org/gitbay/internal/store"
	"gitbay.org/gitbay/internal/web"
)

// A heading precedes the issue's comment thread, matching the merge
// request page, so a screen-reader user skimming by heading has a
// landmark before the first comment rather than falling straight from
// the edit box into the body (#271).
func TestIssuePageHasDiscussionHeading(t *testing.T) {
	var sb strings.Builder
	if err := web.Render(&sb, "issue.html", struct {
		repoPage
		Issue       store.Issue
		BodyHTML    template.HTML
		Comments    []renderedComment
		CanEdit     bool
		CanWrite    bool
		Milestones  []store.Milestone
		Notice      string
		LabelColors map[string]template.CSS
		Draft       *draft
		Reactions   map[int64]reactionBar
	}{repoPage: testRepoPage(), Issue: store.Issue{Number: 1, Title: "bug", Author: "cmc", State: "open"}, Reactions: map[int64]reactionBar{0: {}}}); err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(sb.String(), "<h2>Discussion</h2>") {
		t.Error("no Discussion heading")
	}
}

// sessionCookieFor gives uid a real web session, the way canWriteRepo's
// call to s.viewer(r) needs (internal/httpd/accounts.go:37-47), since
// issueCreateForm gates the milestone/assignee fields on it rather than
// on the handler's own user parameter.
func sessionCookieFor(t *testing.T, s *Server, st *store.Store, uid int64) *http.Cookie {
	t.Helper()
	tok, hash, err := store.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CreateWebSession(hash, uid, time.Hour); err != nil {
		t.Fatal(err)
	}
	return s.sessionCookieFor(tok)
}

// The new-issue form takes milestone and assignee, resolved on the same
// issue create dispatch as the title and labels, so a typo in either
// creates nothing and the label/milestone/assign code paths still run
// notifications and events (#271).
func TestIssueCreateFormHasMilestoneAndAssigneeForWriter(t *testing.T) {
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

	cfg := config.Default()
	cfg.Web.Mode = "accounts"
	s := New(cfg, st, nil)
	req := httptest.NewRequest("GET", "/alice/app/issues/new", nil)
	req.SetPathValue("owner", "alice")
	req.SetPathValue("repo", "app")
	req.AddCookie(sessionCookieFor(t, s, st, uid))
	rr := httptest.NewRecorder()
	s.issueCreateForm(rr, req, u)

	body := rr.Body.String()
	if !strings.Contains(body, `name="labels"`) {
		t.Error("no labels field for a writer")
	}
	if !strings.Contains(body, `name="milestone"`) {
		t.Error("no milestone field for a writer")
	}
	if !strings.Contains(body, `name="assignee"`) {
		t.Error("no assignee field for a writer")
	}
}

// A reader (no write access) sees no milestone/assignee fields, and can
// still create an issue with title and body alone.
func TestIssueCreateFormHidesMilestoneAndAssigneeForReader(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.MigrateUp(); err != nil {
		t.Fatal(err)
	}
	ownerID, err := st.CreateUser("alice", false)
	if err != nil {
		t.Fatal(err)
	}
	readerID, err := st.CreateUser("bob", false)
	if err != nil {
		t.Fatal(err)
	}
	reader := store.User{ID: readerID, Username: "bob"}
	if _, err := st.CreateRepo("user", ownerID, "app", "public"); err != nil {
		t.Fatal(err)
	}

	cfg := config.Default()
	cfg.Web.Mode = "accounts"
	s := New(cfg, st, nil)
	req := httptest.NewRequest("GET", "/alice/app/issues/new", nil)
	req.SetPathValue("owner", "alice")
	req.SetPathValue("repo", "app")
	req.AddCookie(sessionCookieFor(t, s, st, readerID))
	rr := httptest.NewRecorder()
	s.issueCreateForm(rr, req, reader)

	body := rr.Body.String()
	if strings.Contains(body, `name="labels"`) {
		t.Error("reader should not see a labels field")
	}
	if strings.Contains(body, `name="milestone"`) {
		t.Error("reader should not see a milestone field")
	}
	if strings.Contains(body, `name="assignee"`) {
		t.Error("reader should not see an assignee field")
	}

	form := url.Values{"title": {"a bug"}, "body": {"steps"}}
	submit := httptest.NewRequest("POST", "/alice/app/issues/new", strings.NewReader(form.Encode()))
	submit.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	submit.SetPathValue("owner", "alice")
	submit.SetPathValue("repo", "app")
	rr2 := httptest.NewRecorder()
	s.issueCreateSubmit(rr2, submit, reader)
	if rr2.Code != http.StatusSeeOther {
		t.Fatalf("reader create: status %d, body %q", rr2.Code, rr2.Body.String())
	}

	repo, err := st.RepoByPath("alice/app")
	if err != nil {
		t.Fatal(err)
	}
	issue, err := st.IssueByNumber(repo.ID, 1)
	if err != nil {
		t.Fatalf("issue not created: %v", err)
	}
	if issue.Title != "a bug" {
		t.Fatalf("got title %q", issue.Title)
	}
}

// A reader's hand-crafted POST carrying a labels value still creates a
// plain issue: issue create requires write access for --label, so
// issueCreateSubmit drops labels/milestone/assignee from the argv for a
// non-writer rather than sending them and failing the whole create.
func TestIssueCreateSubmitReaderLabelIsDropped(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.MigrateUp(); err != nil {
		t.Fatal(err)
	}
	ownerID, err := st.CreateUser("alice", false)
	if err != nil {
		t.Fatal(err)
	}
	readerID, err := st.CreateUser("bob", false)
	if err != nil {
		t.Fatal(err)
	}
	reader := store.User{ID: readerID, Username: "bob"}
	if _, err := st.CreateRepo("user", ownerID, "app", "public"); err != nil {
		t.Fatal(err)
	}
	repo, err := st.RepoByPath("alice/app")
	if err != nil {
		t.Fatal(err)
	}

	s := New(config.Default(), st, nil)
	form := url.Values{
		"title":  {"a bug"},
		"body":   {"steps"},
		"labels": {"bug"},
	}
	req := httptest.NewRequest("POST", "/alice/app/issues/new", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetPathValue("owner", "alice")
	req.SetPathValue("repo", "app")
	rr := httptest.NewRecorder()
	s.issueCreateSubmit(rr, req, reader)
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("reader create: status %d, body %q", rr.Code, rr.Body.String())
	}

	issue, err := st.IssueByNumber(repo.ID, 1)
	if err != nil {
		t.Fatalf("issue not created: %v", err)
	}
	if len(issue.Labels) != 0 {
		t.Errorf("labels = %v, want none", issue.Labels)
	}
}

// A writer creates an issue with a milestone and an assignee in one
// request; both land on the issue because they go through the same
// dispatch as the create.
func TestIssueCreateSubmitSetsMilestoneAndAssignee(t *testing.T) {
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
	if _, err := st.CreateUser("bob", false); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateRepo("user", uid, "app", "public"); err != nil {
		t.Fatal(err)
	}
	repo, err := st.RepoByPath("alice/app")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateMilestone(repo, "v1", "", ""); err != nil {
		t.Fatal(err)
	}

	s := New(config.Default(), st, nil)
	form := url.Values{
		"title":     {"needs a fix"},
		"body":      {"details"},
		"milestone": {"v1"},
		"assignee":  {"bob"},
	}
	req := httptest.NewRequest("POST", "/alice/app/issues/new", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetPathValue("owner", "alice")
	req.SetPathValue("repo", "app")
	rr := httptest.NewRecorder()
	s.issueCreateSubmit(rr, req, u)
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("status %d, body %q", rr.Code, rr.Body.String())
	}

	issue, err := st.IssueByNumber(repo.ID, 1)
	if err != nil {
		t.Fatalf("issue not created: %v", err)
	}
	if issue.Milestone != "v1" {
		t.Errorf("milestone = %q, want v1", issue.Milestone)
	}
	if len(issue.Assignees) != 1 || issue.Assignees[0] != "bob" {
		t.Errorf("assignees = %v, want [bob]", issue.Assignees)
	}
}

// Preview carries the milestone and assignee back into the form, the same
// way it already carries title and labels, so a writer previewing the
// body does not lose what they picked (#271).
func TestIssueCreateFormPreviewKeepsMilestoneAndAssignee(t *testing.T) {
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

	cfg := config.Default()
	cfg.Web.Mode = "accounts"
	s := New(cfg, st, nil)
	form := url.Values{
		"title":     {"a bug"},
		"body":      {"**steps**"},
		"milestone": {"v1"},
		"assignee":  {"bob"},
		"preview":   {"1"},
	}
	req := httptest.NewRequest("POST", "/alice/app/issues/new", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetPathValue("owner", "alice")
	req.SetPathValue("repo", "app")
	req.AddCookie(sessionCookieFor(t, s, st, uid))
	rr := httptest.NewRecorder()
	s.issueCreateSubmit(rr, req, u)

	body := rr.Body.String()
	if !strings.Contains(body, `value="v1"`) {
		t.Errorf("preview lost the milestone:\n%s", body)
	}
	if !strings.Contains(body, `value="bob"`) {
		t.Errorf("preview lost the assignee:\n%s", body)
	}
}

// A refused create — here a bad milestone — re-renders the new-issue form
// with the draft and a notice, rather than an http.Error page that drops
// everything the visitor typed (#271).
func TestIssueCreateSubmitRefusedKeepsDraft(t *testing.T) {
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
	form := url.Values{
		"title":     {"needs a fix"},
		"body":      {"details"},
		"milestone": {"no-such-milestone"},
	}
	req := httptest.NewRequest("POST", "/alice/app/issues/new", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetPathValue("owner", "alice")
	req.SetPathValue("repo", "app")
	rr := httptest.NewRecorder()
	s.issueCreateSubmit(rr, req, u)

	if rr.Code == http.StatusSeeOther {
		t.Fatalf("expected a failure status, got redirect")
	}
	body := rr.Body.String()
	if !strings.Contains(body, `value="needs a fix"`) {
		t.Errorf("refused create lost the title:\n%s", body)
	}
	if !strings.Contains(body, "details") {
		t.Errorf("refused create lost the body:\n%s", body)
	}
	if !strings.Contains(body, `class="error"`) {
		t.Errorf("refused create has no notice:\n%s", body)
	}

	if _, err := st.IssueByNumber(repo.ID, 1); err == nil {
		t.Fatal("issue was created despite the bad milestone")
	}
}

// A bad assignee creates nothing: issue create resolves the assignee
// before writing the issue, so a typo leaves the repo without a
// half-created issue (#271).
func TestIssueCreateSubmitBadAssigneeCreatesNothing(t *testing.T) {
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
	form := url.Values{
		"title":    {"needs a fix"},
		"body":     {"details"},
		"assignee": {"nobody-such-user"},
	}
	req := httptest.NewRequest("POST", "/alice/app/issues/new", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetPathValue("owner", "alice")
	req.SetPathValue("repo", "app")
	rr := httptest.NewRecorder()
	s.issueCreateSubmit(rr, req, u)
	if rr.Code == http.StatusSeeOther {
		t.Fatalf("expected a failure status, got redirect")
	}
	if !strings.Contains(rr.Body.String(), "nobody-such-user") {
		t.Errorf("error body %q does not name the bad assignee", rr.Body.String())
	}

	if _, err := st.IssueByNumber(repo.ID, 1); err == nil {
		t.Fatal("issue was created despite the bad assignee")
	}
}
