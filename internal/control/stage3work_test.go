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
	git(root, "init", "-q", "-b", "main", "src")
	git(src, "add", ".")
	git(src, "commit", "-q", "-m", "first")
	sha := git(src, "rev-parse", "HEAD")
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
