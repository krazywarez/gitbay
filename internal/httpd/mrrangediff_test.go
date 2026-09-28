package httpd

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"gitbay.org/gitbay/internal/config"
	"gitbay.org/gitbay/internal/control"
	"gitbay.org/gitbay/internal/store"
	"gitbay.org/gitbay/internal/web"
)

// The range-diff page dispatches mr range-diff and renders its text
// output, the same comparison the CLI and iOS already show (#269).
func TestMRRangeDiffPageRendersCommandOutput(t *testing.T) {
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
	if _, err := st.CreateRepo("user", uid, "app", "public"); err != nil {
		t.Fatal(err)
	}

	s := New(config.Default(), st)
	req := httptest.NewRequest("GET", "/alice/app/mrs/1/range-diff", nil)
	req.SetPathValue("owner", "alice")
	req.SetPathValue("repo", "app")
	req.SetPathValue("n", "1")
	rr := httptest.NewRecorder()
	s.mrRangeDiff(rr, req)

	// No merge request 1 exists yet, so this must 404 rather than error.
	if rr.Code != 404 {
		t.Fatalf("status %d, body %s", rr.Code, rr.Body.String())
	}
}

// rangeDiffGitEnv and rangeDiffGitRunner build real git history on disk,
// the way internal/control's own build tests do, since mr range-diff
// runs actual git commands against the repository's bare directory — a
// store-only fixture cannot exercise it.
func rangeDiffGitEnv() []string {
	return append(os.Environ(),
		"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.test",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.test")
}

func rangeDiffGitRunner(t *testing.T) func(dir string, args ...string) string {
	t.Helper()
	env := rangeDiffGitEnv()
	return func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return string(out)
	}
}

// rangeDiffFixture builds a private repository owned by alice with a
// merge request that has two real revisions on disk — an "add b" commit,
// then the same commit amended with one changed line — the same shape
// e2e/rangediff_test.go:14-38 builds for the CLI. bob holds no access to
// the repository, so he stands in for a non-reader.
func rangeDiffFixture(t *testing.T) (st *store.Store, cfg config.Config, alice, bob store.User, repo store.Repo, n int64, v1, v2, title string) {
	t.Helper()
	root := t.TempDir()
	st, err := store.Open(filepath.Join(root, "gitbay.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.MigrateUp(); err != nil {
		t.Fatal(err)
	}
	aliceID, err := st.CreateUser("alice", false)
	if err != nil {
		t.Fatal(err)
	}
	bobID, err := st.CreateUser("bob", false)
	if err != nil {
		t.Fatal(err)
	}
	repoID, err := st.CreateRepo("user", aliceID, "secret", "private")
	if err != nil {
		t.Fatal(err)
	}
	repo, err = st.RepoByID(repoID)
	if err != nil {
		t.Fatal(err)
	}

	git := rangeDiffGitRunner(t)
	dir := control.RepoDir(root, repo.OwnerName, repo.Name)
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		t.Fatal(err)
	}
	git(root, "init", "-q", "--bare", dir)

	src := filepath.Join(root, "src")
	git(root, "init", "-q", "-b", "main", "src")
	os.WriteFile(filepath.Join(src, "a.txt"), []byte("one\n"), 0o644)
	git(src, "add", ".")
	git(src, "commit", "-q", "-m", "base")
	git(src, "push", "-q", dir, "main")

	git(src, "checkout", "-q", "-b", "feat")
	os.WriteFile(filepath.Join(src, "b.txt"), []byte("alpha\nbeta\ngamma\n"), 0o644)
	git(src, "add", ".")
	git(src, "commit", "-q", "-m", "add b")
	git(src, "push", "-q", dir, "feat")
	v1 = strings.TrimSpace(git(src, "rev-parse", "HEAD"))

	os.WriteFile(filepath.Join(src, "b.txt"), []byte("alpha\nbeta revised\ngamma\n"), 0o644)
	git(src, "add", ".")
	git(src, "commit", "-q", "--amend", "--no-edit")
	git(src, "push", "-q", "--force", dir, "feat")
	v2 = strings.TrimSpace(git(src, "rev-parse", "HEAD"))

	title = "range diff of a secret plan"
	n, err = st.CreateMR(repo.ID, aliceID, repo.ID, "feat", "main", title, "", v1, "md", false)
	if err != nil {
		t.Fatal(err)
	}
	mr, err := st.MRByNumber(repo.ID, n)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateMRHead(mr.ID, v2, "", false); err != nil {
		t.Fatal(err)
	}

	cfg = config.Default()
	cfg.Server.Root = root
	cfg.Web.Mode = "accounts"

	alice = store.User{ID: aliceID, Username: "alice"}
	bob = store.User{ID: bobID, Username: "bob"}
	return
}

// loginCookie mints a real web session, the same as a browser login
// would, so a handler under test reads a viewer through s.viewer /
// s.webViewer exactly as it does in production.
func loginCookie(t *testing.T, st *store.Store, userID int64) *http.Cookie {
	t.Helper()
	tok, hash, err := store.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CreateWebSession(hash, userID, time.Hour); err != nil {
		t.Fatal(err)
	}
	return &http.Cookie{Name: sessionCookie, Value: tok}
}

// The range-diff page must answer exactly as the MR page does: the owner
// reads it, and a private repository is 404 — never 403, which would
// confirm the namespace — for both an anonymous caller and a signed-in
// user with no access (#269).
func TestMRRangeDiffPagePrivateRepo(t *testing.T) {
	st, cfg, alice, bob, repo, n, _, _, title := rangeDiffFixture(t)
	s := New(cfg, st)

	newReq := func(cookie *http.Cookie) (*httptest.ResponseRecorder, *http.Request) {
		req := httptest.NewRequest("GET", "/alice/secret/mrs/"+strconv.FormatInt(n, 10)+"/range-diff", nil)
		req.SetPathValue("owner", repo.OwnerName)
		req.SetPathValue("repo", repo.Name)
		req.SetPathValue("n", strconv.FormatInt(n, 10))
		if cookie != nil {
			req.AddCookie(cookie)
		}
		return httptest.NewRecorder(), req
	}

	t.Run("owner reads it", func(t *testing.T) {
		rr, req := newReq(loginCookie(t, st, alice.ID))
		s.mrRangeDiff(rr, req)
		if rr.Code != 200 {
			t.Fatalf("status %d, body %s", rr.Code, rr.Body.String())
		}
		if !strings.Contains(rr.Body.String(), title) {
			t.Errorf("owner's page does not show the MR title %q:\n%s", title, rr.Body.String())
		}
	})

	t.Run("anonymous gets 404 and no title", func(t *testing.T) {
		rr, req := newReq(nil)
		s.mrRangeDiff(rr, req)
		if rr.Code != 404 {
			t.Fatalf("status %d, want 404 (never 403), body %s", rr.Code, rr.Body.String())
		}
		if strings.Contains(rr.Body.String(), title) {
			t.Errorf("404 body leaks the MR title %q:\n%s", title, rr.Body.String())
		}
	})

	t.Run("a signed-in non-reader gets 404 and no title", func(t *testing.T) {
		rr, req := newReq(loginCookie(t, st, bob.ID))
		s.mrRangeDiff(rr, req)
		if rr.Code != 404 {
			t.Fatalf("status %d, want 404 (never 403), body %s", rr.Code, rr.Body.String())
		}
		if strings.Contains(rr.Body.String(), title) {
			t.Errorf("404 body leaks the MR title %q:\n%s", title, rr.Body.String())
		}
	})
}

// --from/--to reach the control command as real argv: an unknown
// revision is refused with not-found, and two real revisions render the
// range-diff between exactly those two.
func TestMRRangeDiffPageFromToQuery(t *testing.T) {
	st, cfg, alice, _, repo, n, v1, v2, _ := rangeDiffFixture(t)
	s := New(cfg, st)
	cookie := loginCookie(t, st, alice.ID)

	newReq := func(query string) (*httptest.ResponseRecorder, *http.Request) {
		req := httptest.NewRequest("GET", "/alice/secret/mrs/"+strconv.FormatInt(n, 10)+"/range-diff"+query, nil)
		req.SetPathValue("owner", repo.OwnerName)
		req.SetPathValue("repo", repo.Name)
		req.SetPathValue("n", strconv.FormatInt(n, 10))
		req.AddCookie(cookie)
		return httptest.NewRecorder(), req
	}

	t.Run("unknown revision is 404", func(t *testing.T) {
		rr, req := newReq("?from=0000000000000000000000000000000000000000")
		s.mrRangeDiff(rr, req)
		if rr.Code != 404 {
			t.Fatalf("status %d, want 404, body %s", rr.Code, rr.Body.String())
		}
	})

	t.Run("two real revisions render the diff between them", func(t *testing.T) {
		rr, req := newReq("?from=" + v1 + "&to=" + v2)
		s.mrRangeDiff(rr, req)
		if rr.Code != 200 {
			t.Fatalf("status %d, body %s", rr.Code, rr.Body.String())
		}
		body := rr.Body.String()
		if !strings.Contains(body, "beta revised") {
			t.Errorf("range-diff does not show the changed line:\n%s", body)
		}
	})

	t.Run("the same revision twice is a usage error rendered inline", func(t *testing.T) {
		rr, req := newReq("?from=" + v1 + "&to=" + v1)
		s.mrRangeDiff(rr, req)
		if rr.Code != 200 {
			t.Fatalf("status %d, want 200 (the error renders on the page), body %s", rr.Code, rr.Body.String())
		}
		if !strings.Contains(rr.Body.String(), "same revision") {
			t.Errorf("page does not show the usage error:\n%s", rr.Body.String())
		}
	})
}

// mrRangeDiffPageData mirrors the anonymous struct mrRangeDiff renders
// with, the way mrPageData mirrors mr's in mrpage_test.go:16-40.
type mrRangeDiffPageData struct {
	repoPage
	MR    store.MR
	Diff  string
	Error string
}

func testRangeDiffMR() store.MR {
	return store.MR{Number: 7, Title: "org native rendering", Author: "cmc"}
}

// The diff branch renders the command's raw text output, HTML-escaped —
// it is not markup, and must not be treated as any.
func TestMRRangeDiffTemplateEscapesDiff(t *testing.T) {
	var sb strings.Builder
	if err := web.Render(&sb, "mrrangediff.html", mrRangeDiffPageData{
		repoPage: testRepoPage(), MR: testRangeDiffMR(),
		Diff: `<script>alert("x")&</script>`,
	}); err != nil {
		t.Fatalf("render: %v", err)
	}
	out := sb.String()
	if strings.Contains(out, "<script>") {
		t.Errorf("diff output was not escaped:\n%s", out)
	}
	if !strings.Contains(out, "&lt;script&gt;") || !strings.Contains(out, "&amp;") {
		t.Errorf("diff output is missing its escaped form:\n%s", out)
	}
}

// One revision has nothing to compare; the page says so rather than
// showing an empty <pre>.
func TestMRRangeDiffTemplateOneRevision(t *testing.T) {
	var sb strings.Builder
	if err := web.Render(&sb, "mrrangediff.html", mrRangeDiffPageData{
		repoPage: testRepoPage(), MR: testRangeDiffMR(),
	}); err != nil {
		t.Fatalf("render: %v", err)
	}
	out := sb.String()
	if !strings.Contains(out, "Nothing to compare") {
		t.Errorf("empty diff does not explain there is nothing to compare:\n%s", out)
	}
	if strings.Contains(out, `<pre class="code`) {
		t.Errorf("empty diff still rendered a pre block:\n%s", out)
	}
}

// A command refusal (same revision twice, in production) renders as a
// page error, not a diff.
func TestMRRangeDiffTemplateError(t *testing.T) {
	var sb strings.Builder
	if err := web.Render(&sb, "mrrangediff.html", mrRangeDiffPageData{
		repoPage: testRepoPage(), MR: testRangeDiffMR(),
		Error: "--from and --to are the same revision",
	}); err != nil {
		t.Fatalf("render: %v", err)
	}
	out := sb.String()
	if !strings.Contains(out, "same revision") {
		t.Errorf("error was not rendered:\n%s", out)
	}
	if strings.Contains(out, `<pre class="code`) {
		t.Errorf("error state still rendered a diff block:\n%s", out)
	}
}
