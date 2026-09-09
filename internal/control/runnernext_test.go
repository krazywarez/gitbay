package control

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gitbay.org/gitbay/internal/config"
	"gitbay.org/gitbay/internal/protocol"
	"gitbay.org/gitbay/internal/store"
)

// runnerCtx builds a Ctx good enough to run runRunnerNext directly: an
// admin user (runnerSession accepts admin as well as scope "runner"), a
// server root that matches where the test's bare repo lives.
func runnerCtx(st *store.Store, uid int64, root string) (*Ctx, *bytes.Buffer) {
	var out bytes.Buffer
	fp := fmt.Sprintf("SHA256:runner-%d", uid)
	st.AddSSHKey(uid, fp, "ssh-ed25519", []byte(fp), "full") // ErrDuplicateKey on reuse is fine
	c := &Ctx{
		User:   store.User{ID: uid, Username: "ci", IsAdmin: true},
		Scope:  "full",
		Source: fp,
		Store:  st,
		Cfg:    config.Config{Server: config.Server{Root: root, SiteURL: "https://x.test"}},
		Stdin:  strings.NewReader(""),
		Stdout: &out,
		Stderr: &out,
	}
	return c, &out
}

// A queued build's sha is orphaned by rewinding "main" past it — the shape
// a force-push leaves, without needing an actual git-receive-pack round
// trip. The base commit stays reachable, giving one real build behind it.
func setupOrphanRepo(t *testing.T) (*store.Store, store.Repo, int64, string, string, string) {
	t.Helper()
	st, repo, uid := newQueueTestRepo(t)
	git := gitRunner(t)
	root := t.TempDir()

	src := filepath.Join(root, "src")
	os.MkdirAll(src, 0o755)
	git(root, "init", "-q", "-b", "main", "src")
	git(src, "commit", "-q", "--allow-empty", "-m", "base")
	baseSHA := strings.TrimSpace(git(src, "rev-parse", "HEAD"))
	git(src, "commit", "-q", "--allow-empty", "-m", "orphaned")
	orphanSHA := strings.TrimSpace(git(src, "rev-parse", "HEAD"))

	dir := RepoDir(root, repo.OwnerName, repo.Name)
	os.MkdirAll(filepath.Dir(dir), 0o755)
	git(root, "clone", "-q", "--bare", src, dir)
	git(dir, "update-ref", "refs/heads/main", baseSHA)

	return st, repo, uid, root, baseSHA, orphanSHA
}

// A build queued for a sha a force-push orphaned is cancelled at claim
// time, and the runner gets the next real build instead of an impossible
// one.
func TestRunnerNextSkipsOrphanedBuildAndClaimsNext(t *testing.T) {
	st, repo, uid, root, baseSHA, orphanSHA := setupOrphanRepo(t)

	orphanedID, err := st.CreateBuild(repo.ID, "unit", orphanSHA, "main", "[]", "", "", true)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetCommitStatus(repo.ID, orphanSHA, "ci/unit", "pending", "queued", "https://x.test", uid); err != nil {
		t.Fatal(err)
	}
	realID, err := st.CreateBuild(repo.ID, "unit", baseSHA, "main", "[]", "", "", true)
	if err != nil {
		t.Fatal(err)
	}

	c, out := runnerCtx(st, uid, root)
	code := runRunnerNext(c, nil)
	if code != protocol.ExitOK {
		t.Fatalf("runner next: exit %d, output:\n%s", code, out.String())
	}
	if !strings.Contains(out.String(), fmt.Sprintf("build %d: ", realID)) || !strings.Contains(out.String(), baseSHA[:10]) {
		t.Fatalf("expected the real build handed out, got:\n%s", out.String())
	}

	orphaned, err := st.BuildByNumber(repo.ID, orphanedID)
	if err != nil {
		t.Fatal(err)
	}
	if orphaned.Status != "cancelled" {
		t.Fatalf("orphaned build status = %q, want cancelled", orphaned.Status)
	}
	log, _ := st.BuildLog(orphaned.ID)
	if !strings.Contains(string(log), "not reachable") {
		t.Fatalf("orphaned build log missing the reason:\n%s", log)
	}
	statuses, err := st.ListCommitStatuses(repo.ID, orphanSHA)
	if err != nil {
		t.Fatal(err)
	}
	if len(statuses) != 1 || statuses[0].State == "pending" {
		t.Fatalf("orphaned build's commit status still pending: %+v", statuses)
	}

	real, err := st.BuildByNumber(repo.ID, realID)
	if err != nil {
		t.Fatal(err)
	}
	if real.Status != "running" {
		t.Fatalf("real build status = %q, want running (claimed)", real.Status)
	}
}

// A build whose sha is genuinely reachable is claimed exactly as before:
// the reachability check must never reject a healthy build.
func TestRunnerNextClaimsReachableBuildNormally(t *testing.T) {
	st, repo, uid, root, baseSHA, _ := setupOrphanRepo(t)
	id, err := st.CreateBuild(repo.ID, "unit", baseSHA, "main", "[]", "", "", true)
	if err != nil {
		t.Fatal(err)
	}

	c, out := runnerCtx(st, uid, root)
	code := runRunnerNext(c, nil)
	if code != protocol.ExitOK {
		t.Fatalf("runner next: exit %d, output:\n%s", code, out.String())
	}
	if strings.Contains(out.String(), "no pending builds") {
		t.Fatalf("a reachable build was not handed out:\n%s", out.String())
	}
	b, err := st.BuildByNumber(repo.ID, id)
	if err != nil {
		t.Fatal(err)
	}
	if b.Status != "running" {
		t.Fatalf("reachable build status = %q, want running", b.Status)
	}
	log, _ := st.BuildLog(b.ID)
	if strings.Contains(string(log), "cancelled") {
		t.Fatalf("a healthy build was cancelled:\n%s", log)
	}
}

// A queue built entirely of one orphaned sha, past the loop's cap, still
// terminates and reports no pending builds — never a spin, never an
// error — while everything up to the cap is actually resolved rather than
// left claimed and dangling.
func TestRunnerNextOrphanedQueuePastCapReportsNoPendingBuilds(t *testing.T) {
	st, repo, uid, root, _, orphanSHA := setupOrphanRepo(t)
	total := maxOrphanSkip + 1
	for i := 0; i < total; i++ {
		if _, err := st.CreateBuild(repo.ID, fmt.Sprintf("job%d", i), orphanSHA, "main", "[]", "", "", true); err != nil {
			t.Fatal(err)
		}
	}

	c, out := runnerCtx(st, uid, root)
	code := runRunnerNext(c, nil)
	if code != protocol.ExitOK {
		t.Fatalf("runner next: exit %d, output:\n%s", code, out.String())
	}
	if !strings.Contains(out.String(), "no pending builds") {
		t.Fatalf("expected no pending builds, got:\n%s", out.String())
	}

	builds, err := st.ListBuilds(repo.ID, total+1)
	if err != nil {
		t.Fatal(err)
	}
	var cancelled, pending, other int
	for _, b := range builds {
		switch b.Status {
		case "cancelled":
			cancelled++
		case "pending":
			pending++
		default:
			other++
		}
	}
	if other != 0 {
		t.Fatalf("a build was left claimed rather than resolved: cancelled=%d pending=%d other=%d", cancelled, pending, other)
	}
	if cancelled != maxOrphanSkip {
		t.Fatalf("cancelled %d builds, want the cap of %d", cancelled, maxOrphanSkip)
	}
	if pending != total-maxOrphanSkip {
		t.Fatalf("pending %d builds, want %d left behind by the cap", pending, total-maxOrphanSkip)
	}
}

// Reachable erroring — no repository on disk at all — must not read as
// "unreachable": the ambiguous case is claimable, never cancelled.
func TestRunnerNextClaimsBuildWhenReachabilityCannotBeChecked(t *testing.T) {
	st, repo, uid := newQueueTestRepo(t)
	// No RepoDir created on disk at all: Reachable will fail to even stat
	// the repository, which must not be read as "orphaned".
	root := t.TempDir()
	id, err := st.CreateBuild(repo.ID, "unit", strings.Repeat("a", 40), "main", "[]", "", "", true)
	if err != nil {
		t.Fatal(err)
	}

	c, out := runnerCtx(st, uid, root)
	code := runRunnerNext(c, nil)
	if code != protocol.ExitOK {
		t.Fatalf("runner next: exit %d, output:\n%s", code, out.String())
	}
	if strings.Contains(out.String(), "no pending builds") {
		t.Fatalf("a build was not handed out when reachability could not be checked:\n%s", out.String())
	}
	b, err := st.BuildByNumber(repo.ID, id)
	if err != nil {
		t.Fatal(err)
	}
	if b.Status != "running" {
		t.Fatalf("build status = %q, want running: an unchecked build must still be claimable", b.Status)
	}
}

// runner log records when the stream ended, so a build whose runner then
// vanishes is failed within minutes rather than at the deadline (#179).
func TestRunnerLogMarksStreamClosed(t *testing.T) {
	st, repo, uid := newQueueTestRepo(t)
	root := t.TempDir()
	if _, err := st.CreateBuild(repo.ID, "unit", strings.Repeat("a", 40), "main", "[]", "", "", true); err != nil {
		t.Fatal(err)
	}
	b, ok, err := st.ClaimBuild([]int64{repo.ID}, false)
	if err != nil || !ok {
		t.Fatalf("claim: %v", err)
	}
	c, _ := runnerCtx(st, uid, root)
	c.Stdin = strings.NewReader("hello\n") // one chunk, then EOF: the stream ends
	if code := runRunnerLog(c, []string{fmt.Sprint(b.ID)}); code != 0 {
		t.Fatalf("runner log exited %d", code)
	}
	got, _ := st.BuildByID(b.ID)
	if got.LogClosedAt == "" {
		t.Fatal("log_closed_at not set when the stream ended")
	}
	if got.Status != "running" {
		t.Errorf("status %s, want still running until the runner reports", got.Status)
	}
}
