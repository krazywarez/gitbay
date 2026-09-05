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

const testZeroSHA = "0000000000000000000000000000000000000000"

// gitTestEnv sets up an isolated git identity so tests never touch a
// developer's real config.
func gitTestEnv() []string {
	return append(os.Environ(),
		"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.test",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.test")
}

func gitRunner(t *testing.T) func(dir string, args ...string) string {
	t.Helper()
	env := gitTestEnv()
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

// newQueueTestRepo returns a store with one public repo (default branch
// "main", matching the schema default) and the uid to queue builds as.
func newQueueTestRepo(t *testing.T) (*store.Store, store.Repo, int64) {
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
	repoID, err := st.CreateRepo("user", uid, "app", "public")
	if err != nil {
		t.Fatal(err)
	}
	repo, err := st.RepoByID(repoID)
	if err != nil {
		t.Fatal(err)
	}
	return st, repo, uid
}

// A DiffFiles failure must not turn into a silent skip: when the old sha
// on record cannot be diffed against, every job runs regardless of what
// it names in paths, the same as when there is no diff base at all.
func TestQueueBranchBuildsFailsOpenOnDiffFailure(t *testing.T) {
	st, repo, uid := newQueueTestRepo(t)
	git := gitRunner(t)

	root := t.TempDir()

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

// A new branch's first push carries no old sha, but a diff base still
// exists: the merge base with the default branch. A push whose commits
// only touch paths a job ignores must not queue that job, or path
// filters never do anything on the ordinary branch-then-MR workflow.
func TestQueueBranchBuildsNewBranchIgnoredPathSkips(t *testing.T) {
	st, repo, uid := newQueueTestRepo(t)
	git := gitRunner(t)
	root := t.TempDir()

	src := filepath.Join(root, "src")
	os.MkdirAll(filepath.Join(src, ".gitbay"), 0o755)
	os.MkdirAll(filepath.Join(src, "docs"), 0o755)
	os.WriteFile(filepath.Join(src, ".gitbay", "ci.yml"), []byte(
		"jobs:\n  unit:\n    paths-ignore:\n      - docs/**\n    steps:\n      - echo hi\n"), 0o644)
	os.WriteFile(filepath.Join(src, "docs", "x.md"), []byte("# x\n"), 0o644)
	git(root, "init", "-q", "-b", "main", "src")
	git(src, "add", ".")
	git(src, "commit", "-q", "-m", "base")

	git(src, "checkout", "-q", "-b", "feature")
	os.WriteFile(filepath.Join(src, "docs", "x.md"), []byte("# x changed\n"), 0o644)
	git(src, "add", ".")
	git(src, "commit", "-q", "-m", "docs only")
	featureSHA := strings.TrimSpace(git(src, "rev-parse", "HEAD"))

	dir := RepoDir(root, repo.OwnerName, repo.Name)
	os.MkdirAll(filepath.Dir(dir), 0o755)
	git(root, "clone", "-q", "--bare", src, dir)

	QueueBranchBuilds(st, root, "https://x.test", repo, uid, "feature", testZeroSHA, featureSHA, time.Now())

	builds, err := st.ListBuilds(repo.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(builds) != 0 {
		t.Fatalf("docs-only push on a new branch queued a build: %+v", builds)
	}
}

// The same new-branch push, but touching a path the job cares about:
// the merge-base diff must still let it through.
func TestQueueBranchBuildsNewBranchMatchedPathQueues(t *testing.T) {
	st, repo, uid := newQueueTestRepo(t)
	git := gitRunner(t)
	root := t.TempDir()

	src := filepath.Join(root, "src")
	os.MkdirAll(filepath.Join(src, ".gitbay"), 0o755)
	os.MkdirAll(filepath.Join(src, "src"), 0o755)
	os.WriteFile(filepath.Join(src, ".gitbay", "ci.yml"), []byte(
		"jobs:\n  unit:\n    paths:\n      - src/**\n    steps:\n      - echo hi\n"), 0o644)
	os.WriteFile(filepath.Join(src, "src", "x.go"), []byte("package x\n"), 0o644)
	git(root, "init", "-q", "-b", "main", "src")
	git(src, "add", ".")
	git(src, "commit", "-q", "-m", "base")

	git(src, "checkout", "-q", "-b", "feature")
	os.WriteFile(filepath.Join(src, "src", "x.go"), []byte("package x\n\nvar y int\n"), 0o644)
	git(src, "add", ".")
	git(src, "commit", "-q", "-m", "src change")
	featureSHA := strings.TrimSpace(git(src, "rev-parse", "HEAD"))

	dir := RepoDir(root, repo.OwnerName, repo.Name)
	os.MkdirAll(filepath.Dir(dir), 0o755)
	git(root, "clone", "-q", "--bare", src, dir)

	QueueBranchBuilds(st, root, "https://x.test", repo, uid, "feature", testZeroSHA, featureSHA, time.Now())

	builds, err := st.ListBuilds(repo.ID, 10)
	if err != nil || len(builds) != 1 {
		t.Fatalf("builds after queue: %v %v", builds, err)
	}
	if builds[0].Job != "unit" || builds[0].SHA != featureSHA {
		t.Fatalf("queued build wrong: %+v", builds[0])
	}
}

// When the new branch shares no history with the default branch, the
// merge base cannot be computed. That must fail open, same as any other
// diff base that cannot be evaluated.
func TestQueueBranchBuildsNewBranchFailsOpenWithoutMergeBase(t *testing.T) {
	st, repo, uid := newQueueTestRepo(t)
	git := gitRunner(t)
	root := t.TempDir()

	dir := RepoDir(root, repo.OwnerName, repo.Name)
	os.MkdirAll(filepath.Dir(dir), 0o755)
	git(root, "init", "-q", "--bare", dir)

	mainSrc := filepath.Join(root, "main-src")
	os.MkdirAll(filepath.Join(mainSrc, ".gitbay"), 0o755)
	os.WriteFile(filepath.Join(mainSrc, ".gitbay", "ci.yml"), []byte(
		"jobs:\n  unit:\n    paths-ignore:\n      - docs/**\n    steps:\n      - echo hi\n"), 0o644)
	git(root, "init", "-q", "-b", "main", "main-src")
	git(mainSrc, "add", ".")
	git(mainSrc, "commit", "-q", "-m", "base")
	git(mainSrc, "push", "-q", dir, "main")

	// An unrelated repository: no common commit with main.
	otherSrc := filepath.Join(root, "other-src")
	os.MkdirAll(filepath.Join(otherSrc, ".gitbay"), 0o755)
	os.MkdirAll(filepath.Join(otherSrc, "docs"), 0o755)
	os.WriteFile(filepath.Join(otherSrc, ".gitbay", "ci.yml"), []byte(
		"jobs:\n  unit:\n    paths-ignore:\n      - docs/**\n    steps:\n      - echo hi\n"), 0o644)
	os.WriteFile(filepath.Join(otherSrc, "docs", "x.md"), []byte("# x\n"), 0o644)
	git(root, "init", "-q", "-b", "feature", "other-src")
	git(otherSrc, "add", ".")
	git(otherSrc, "commit", "-q", "-m", "unrelated docs-only")
	otherSHA := strings.TrimSpace(git(otherSrc, "rev-parse", "HEAD"))
	git(otherSrc, "push", "-q", dir, "feature")

	QueueBranchBuilds(st, root, "https://x.test", repo, uid, "feature", testZeroSHA, otherSHA, time.Now())

	builds, err := st.ListBuilds(repo.ID, 10)
	if err != nil || len(builds) != 1 {
		t.Fatalf("expected fail-open to queue the job: %v %v", builds, err)
	}
}

// The first push to a brand-new repository moves the default branch
// itself with no prior commit: the merge base of the default branch
// against its own tip is the tip, which carries no diff. That must
// fail open rather than read as "nothing changed".
func TestQueueBranchBuildsFreshDefaultBranchFailsOpen(t *testing.T) {
	st, repo, uid := newQueueTestRepo(t)
	git := gitRunner(t)
	root := t.TempDir()

	src := filepath.Join(root, "src")
	os.MkdirAll(filepath.Join(src, ".gitbay"), 0o755)
	os.MkdirAll(filepath.Join(src, "docs"), 0o755)
	os.WriteFile(filepath.Join(src, ".gitbay", "ci.yml"), []byte(
		"jobs:\n  unit:\n    paths:\n      - src/**\n    steps:\n      - echo hi\n"), 0o644)
	os.WriteFile(filepath.Join(src, "docs", "x.md"), []byte("# x\n"), 0o644)
	git(root, "init", "-q", "-b", "main", "src")
	git(src, "add", ".")
	git(src, "commit", "-q", "-m", "initial")
	sha := strings.TrimSpace(git(src, "rev-parse", "HEAD"))

	dir := RepoDir(root, repo.OwnerName, repo.Name)
	os.MkdirAll(filepath.Dir(dir), 0o755)
	git(root, "clone", "-q", "--bare", src, dir)

	QueueBranchBuilds(st, root, "https://x.test", repo, uid, "main", testZeroSHA, sha, time.Now())

	builds, err := st.ListBuilds(repo.ID, 10)
	if err != nil || len(builds) != 1 {
		t.Fatalf("expected fail-open on the repository's first commit: %v %v", builds, err)
	}
}

// An ordinary push with a genuine old sha is unaffected by the
// new-branch handling: filtering still works exactly as it did before.
func TestQueueBranchBuildsOrdinaryPushStillFilters(t *testing.T) {
	st, repo, uid := newQueueTestRepo(t)
	git := gitRunner(t)
	root := t.TempDir()

	src := filepath.Join(root, "src")
	os.MkdirAll(filepath.Join(src, ".gitbay"), 0o755)
	os.MkdirAll(filepath.Join(src, "docs"), 0o755)
	os.WriteFile(filepath.Join(src, ".gitbay", "ci.yml"), []byte(
		"jobs:\n  unit:\n    paths-ignore:\n      - docs/**\n    steps:\n      - echo hi\n"), 0o644)
	os.WriteFile(filepath.Join(src, "docs", "x.md"), []byte("# x\n"), 0o644)
	git(root, "init", "-q", "-b", "main", "src")
	git(src, "add", ".")
	git(src, "commit", "-q", "-m", "base")
	oldSHA := strings.TrimSpace(git(src, "rev-parse", "HEAD"))

	os.WriteFile(filepath.Join(src, "docs", "x.md"), []byte("# x changed\n"), 0o644)
	git(src, "add", ".")
	git(src, "commit", "-q", "-m", "docs only")
	newSHA := strings.TrimSpace(git(src, "rev-parse", "HEAD"))

	dir := RepoDir(root, repo.OwnerName, repo.Name)
	os.MkdirAll(filepath.Dir(dir), 0o755)
	git(root, "clone", "-q", "--bare", src, dir)

	QueueBranchBuilds(st, root, "https://x.test", repo, uid, "main", oldSHA, newSHA, time.Now())

	builds, err := st.ListBuilds(repo.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(builds) != 0 {
		t.Fatalf("docs-only push with a real old sha queued a build: %+v", builds)
	}
}

// QueueMRBuilds keeps failing open with no diff base at all: deriving a
// merge base for the MR head is deliberately out of scope here (#172 —
// filtering a head down to zero jobs leaves it with no statuses, which
// the require_checks gate reads as unmergeable). This test documents
// and locks in that choice.
func TestQueueMRBuildsStillFailsOpen(t *testing.T) {
	st, repo, uid := newQueueTestRepo(t)
	git := gitRunner(t)
	root := t.TempDir()

	src := filepath.Join(root, "src")
	os.MkdirAll(filepath.Join(src, ".gitbay"), 0o755)
	os.MkdirAll(filepath.Join(src, "docs"), 0o755)
	os.WriteFile(filepath.Join(src, ".gitbay", "ci.yml"), []byte(
		"jobs:\n  unit:\n    paths-ignore:\n      - docs/**\n    steps:\n      - echo hi\n"), 0o644)
	os.WriteFile(filepath.Join(src, "docs", "x.md"), []byte("# x\n"), 0o644)
	git(root, "init", "-q", "-b", "main", "src")
	git(src, "add", ".")
	git(src, "commit", "-q", "-m", "base")

	git(src, "checkout", "-q", "-b", "pr")
	os.WriteFile(filepath.Join(src, "docs", "x.md"), []byte("# x changed\n"), 0o644)
	git(src, "add", ".")
	git(src, "commit", "-q", "-m", "docs only")
	prSHA := strings.TrimSpace(git(src, "rev-parse", "HEAD"))

	dir := RepoDir(root, repo.OwnerName, repo.Name)
	os.MkdirAll(filepath.Dir(dir), 0o755)
	git(root, "clone", "-q", "--bare", src, dir)
	git(dir, "update-ref", "refs/merge-requests/1/head", prSHA)

	QueueMRBuilds(st, root, "https://x.test", repo, uid, 1, prSHA)

	builds, err := st.ListBuilds(repo.ID, 10)
	if err != nil || len(builds) != 1 {
		t.Fatalf("expected the MR head to fail open and queue a build: %v %v", builds, err)
	}
}
