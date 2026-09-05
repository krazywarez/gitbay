package control

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gitbay.org/gitbay/internal/store"
)

// A DiffFiles failure must not turn into a silent skip: when the old sha
// on record cannot be diffed against, every job runs regardless of what
// it names in paths, the same as when there is no diff base at all.
func TestQueueBranchBuildsFailsOpenOnDiffFailure(t *testing.T) {
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
	repoID, err := st.CreateRepo("user", uid, "app", "public")
	if err != nil {
		t.Fatal(err)
	}
	repo, err := st.RepoByID(repoID)
	if err != nil {
		t.Fatal(err)
	}

	root := t.TempDir()
	env := append(os.Environ(),
		"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.test",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.test")
	git := func(dir string, args ...string) string {
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

	// A job whose paths would exclude a docs-only change, so the test
	// proves something: without fail-open, the diff failure would leave
	// the filter unevaluated and this build would never queue.
	src := filepath.Join(root, "src")
	os.MkdirAll(filepath.Join(src, ".gitbay"), 0o755)
	os.MkdirAll(filepath.Join(src, "docs"), 0o755)
	os.WriteFile(filepath.Join(src, ".gitbay", "ci.yml"), []byte(
		"jobs:\n  unit:\n    paths:\n      - src/**\n    steps:\n      - echo hi\n"), 0o644)
	os.WriteFile(filepath.Join(src, "docs", "x.md"), []byte("# x\n"), 0o644)
	git(root, "init", "-q", "-b", "main", "src")
	git(src, "add", ".")
	git(src, "commit", "-q", "-m", "base")
	os.WriteFile(filepath.Join(src, "docs", "x.md"), []byte("# x changed\n"), 0o644)
	git(src, "add", ".")
	git(src, "commit", "-q", "-m", "docs only")
	newSHA := strings.TrimSpace(git(src, "rev-parse", "HEAD"))

	dir := RepoDir(root, repo.OwnerName, repo.Name)
	os.MkdirAll(filepath.Dir(dir), 0o755)
	git(root, "clone", "-q", "--bare", src, dir)

	// Well-formed but names no object in this repo: git diff itself
	// fails, rather than the empty/all-zero short-circuit HasDiffBase
	// already covers.
	old := strings.Repeat("1", 40)

	QueueBranchBuilds(st, root, "https://x.test", repo, uid, "main", old, newSHA, time.Now())

	builds, err := st.ListBuilds(repo.ID, 10)
	if err != nil || len(builds) != 1 {
		t.Fatalf("builds after queue: %v %v", builds, err)
	}
	if builds[0].Job != "unit" || builds[0].Status != "pending" || builds[0].SHA != newSHA {
		t.Fatalf("queued build wrong: %+v", builds[0])
	}
}
