package control

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gitbay.org/gitbay/internal/store"
)

// workFixture is a repository on disk with a CI job and an issue
// template, an issue, a merge request, a commit status and a saved
// query, all as alice. It returns the head sha.
func workFixture(t *testing.T) (*store.Store, store.Repo, store.User, string, string) {
	t.Helper()
	st, repo, uid := newQueueTestRepo(t)
	u := store.User{ID: uid, Username: "alice"}
	root := t.TempDir()
	env := append(gitTestEnv(), "GIT_AUTHOR_DATE=2026-09-01T10:00:00Z", "GIT_COMMITTER_DATE=2026-09-01T10:00:00Z")
	git := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir, cmd.Env = dir, env
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	src := filepath.Join(root, "src")
	if err := os.MkdirAll(filepath.Join(src, ".gitbay"), 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(src, ".gitbay", "ci.yml"), []byte("jobs:\n  test:\n    steps:\n      - go test ./...\n"), 0o644)
	os.WriteFile(filepath.Join(src, ".gitbay", "issue-template-bug.md"), []byte("## Steps\n"), 0o644)
	os.MkdirAll(filepath.Join(src, ".gitbay", "wiki"), 0o755)
	os.WriteFile(filepath.Join(src, ".gitbay", "wiki", "Home.md"), []byte("# Welcome\n\nStart here.\n"), 0o644)
	git(root, "init", "-q", "-b", "main", "src")
	git(src, "add", ".")
	git(src, "commit", "-q", "-m", "first")
	sha := git(src, "rev-parse", "HEAD")
	git(src, "tag", "v1.0.0")
	dir := RepoDir(root, repo.OwnerName, repo.Name)
	os.MkdirAll(filepath.Dir(dir), 0o755)
	git(root, "clone", "-q", "--bare", src, dir)

	p := repo.Path()
	dispatchIn(t, st, u, root, "", "issue", "create", p, "--title", "Crash on start", "--body", "It crashes.")
	if _, err := st.CreateMR(repo.ID, uid, repo.ID, "fix", "main", "Fix crash", "", sha, "md", false); err != nil {
		t.Fatal(err)
	}
	dispatchIn(t, st, u, root, "", "status", "set", p, sha, "--context", "ext/test", "--state", "success", "--description", "all green")
	dispatchIn(t, st, u, root, "", "query", "save", "open", "is:open")
	return st, repo, u, root, sha
}

func TestWorkPlainPinnedA(t *testing.T) {
	st, repo, u, root, sha := workFixture(t)
	p := repo.Path()
	for name, argv := range map[string][]string{
		"query-run":       {"query", "run", "open"},
		"issue-list-q":    {"issue", "list", "--q", "repo:alice/app is:open"},
		"query-list":      {"query", "list"},
		"query-show":      {"query", "show", "open"},
		"search":          {"search", "crash"},
		"feed":            {"feed"},
		"mr-revisions":    {"mr", "revisions", p, "1"},
		"issue-templates": {"issue", "templates", p},
		"build-jobs":      {"build", "jobs", p},
		"status-list":     {"status", "list", p, sha},
	} {
		pinPlain(t, name, dispatchIn(t, st, u, root, "", argv...))
	}
}

func TestWorkScreensA(t *testing.T) {
	st, repo, u, root, sha := workFixture(t)
	p := repo.Path()
	for _, tc := range []struct {
		argv []string
		want []string
	}{
		{[]string{"query", "run", "open"}, []string{"open (2)\n", "alice/app#1", "Crash on start", "alice/app!1"}},
		{[]string{"issue", "list", "--q", "repo:alice/app is:open"}, []string{"Results (1)\n", "Crash on start"}},
		{[]string{"query", "list"}, []string{"Saved queries (1)\n", "open  is:open"}},
		{[]string{"query", "show", "open"}, []string{"Query:", "is:open", "Matches:", "2"}},
		{[]string{"search", "crash"}, []string{"Issues (1)\n", "Merge requests (1)\n", "Crash on start"}},
		{[]string{"feed"}, []string{"Activity ("}},
		{[]string{"mr", "revisions", p, "1"}, []string{"Revisions (1)\n", "v1"}},
		{[]string{"issue", "templates", p}, []string{"Issue templates (1)\nissue-template-bug.md"}},
		{[]string{"build", "jobs", p}, []string{"Jobs (1)\ntest  on push\n"}},
		{[]string{"status", "list", p, sha}, []string{"Commit:", "Combined:", "✓", "Statuses (1)\n", "ext/test  all green"}},
	} {
		out := atTerminalIn(t, st, u, root, tc.argv...)
		for _, w := range tc.want {
			if !strings.Contains(out, w) {
				t.Errorf("%v: missing %q in:\n%s", tc.argv, w, out)
			}
		}
		if tc.argv[0] != "feed" {
			checkLegend(t, out)
		}
	}
}

func TestWorkPlainPinnedB(t *testing.T) {
	st, repo, u, root, _ := workFixture(t)
	p := repo.Path()
	dispatchIn(t, st, u, root, "", "release", "create", p, "v1.0.0", "--title", "v1.0.0 — first", "--notes", "First release.")
	dispatchIn(t, st, u, root, "package main\n", "snippet", "create", "main.go", "--description", "Hello")
	snippets := dispatchIn(t, st, u, root, "", "snippet", "list")
	id, _, _ := strings.Cut(snippets, "\t")
	for name, argv := range map[string][]string{
		"release-list": {"release", "list", p},
		"release-show": {"release", "show", p, "v1.0.0"},
		"snippet-list": {"snippet", "list"},
		"snippet-show": {"snippet", "show", id},
		"wiki-list":    {"wiki", "list", p},
		"wiki-show":    {"wiki", "show", p, "Home"},
		"explore":      {"explore"},
	} {
		got := dispatchIn(t, st, u, root, "", argv...)
		pinPlain(t, name, strings.ReplaceAll(got, id, "<id>"))
	}
}

func TestWorkScreensB(t *testing.T) {
	st, repo, u, root, _ := workFixture(t)
	p := repo.Path()
	dispatchIn(t, st, u, root, "", "release", "create", p, "v1.0.0", "--title", "v1.0.0 — first", "--notes", "First release.")
	dispatchIn(t, st, u, root, "package main\n", "snippet", "create", "main.go", "--description", "Hello")
	snippets := dispatchIn(t, st, u, root, "", "snippet", "list")
	id, _, _ := strings.Cut(snippets, "\t")
	for _, tc := range []struct {
		argv []string
		want []string
	}{
		{[]string{"release", "list", p}, []string{"Releases (1)\n", "v1.0.0  first"}},
		{[]string{"release", "show", p, "v1.0.0"}, []string{"Release:", "v1.0.0  v1.0.0 — first", "First release."}},
		{[]string{"snippet", "list"}, []string{"Snippets (1)\n", "Hello", "main.go"}},
		{[]string{"snippet", "show", id}, []string{"Snippet:", "Hello", "Files (1)\nmain.go"}},
		{[]string{"wiki", "list", p}, []string{"Wiki (1)\nHome", "home"}},
		{[]string{"wiki", "show", p, "Home"}, []string{"Page:", "Home", "Welcome", "Start here."}},
		{[]string{"explore"}, []string{"Explore (1)\nalice/app"}},
	} {
		out := atTerminalIn(t, st, u, root, tc.argv...)
		for _, w := range tc.want {
			if !strings.Contains(out, w) {
				t.Errorf("%v: missing %q in:\n%s", tc.argv, w, out)
			}
		}
		checkLegend(t, out)
	}
}

func TestAuditScreen(t *testing.T) {
	st, _, uid := newQueueTestRepo(t)
	admin := store.User{ID: uid, Username: "alice", IsAdmin: true}
	dispatchAs(t, st, admin, "", "admin", "user", "create", "bob")
	pinPlain(t, "audit", dispatchAs(t, st, admin, "", "audit"))
	out := atTerminal(t, st, admin, "audit")
	if !strings.Contains(out, "Audit (") || !strings.Contains(out, "alice") {
		t.Errorf("audit:\n%s", out)
	}
	checkLegend(t, out)
}
