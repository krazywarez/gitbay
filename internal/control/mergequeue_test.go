package control

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"gitbay.org/gitbay/internal/config"
	"gitbay.org/gitbay/internal/protocol"
	"gitbay.org/gitbay/internal/store"
)

// queueFixture is a repository on disk with one merge request, !1,
// feature into main, whose head is one commit ahead of main.
type queueFixture struct {
	t        *testing.T
	st       *store.Store
	repo     store.Repo
	alice    store.User // the owner
	root     string
	dir      string
	src      string
	git      func(dir string, args ...string) string
	headSHA  string
	targetSH string
}

func newQueueFixture(t *testing.T, set func(*store.RepoSettings)) *queueFixture {
	t.Helper()
	st, repo, uid := newQueueTestRepo(t)
	if set != nil {
		if _, err := st.UpdateRepoSettings(repo.ID, set); err != nil {
			t.Fatal(err)
		}
		var err error
		if repo, err = st.RepoByID(repo.ID); err != nil {
			t.Fatal(err)
		}
	}
	f := &queueFixture{t: t, st: st, repo: repo, alice: store.User{ID: uid, Username: "alice"},
		root: t.TempDir(), git: gitRunner(t)}
	f.src = filepath.Join(f.root, "src")
	f.git(f.root, "init", "-q", "-b", "main", "src")
	f.write("README", "x\n")
	f.git(f.src, "add", ".")
	f.git(f.src, "commit", "-q", "-m", "base")
	f.targetSH = strings.TrimSpace(f.git(f.src, "rev-parse", "HEAD"))
	f.git(f.src, "checkout", "-q", "-b", "feature")
	f.write("feature.txt", "y\n")
	f.git(f.src, "add", ".")
	f.git(f.src, "commit", "-q", "-m", "change")
	f.headSHA = strings.TrimSpace(f.git(f.src, "rev-parse", "HEAD"))

	f.dir = RepoDir(f.root, repo.OwnerName, repo.Name)
	os.MkdirAll(filepath.Dir(f.dir), 0o755)
	f.git(f.root, "clone", "-q", "--bare", f.src, f.dir)
	f.git(f.dir, "update-ref", mrHeadRef(1), f.headSHA)
	if _, err := st.CreateMR(repo.ID, uid, repo.ID, "feature", "main", "t", "", f.headSHA, "md", false); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *queueFixture) write(name, body string) {
	f.t.Helper()
	if err := os.WriteFile(filepath.Join(f.src, name), []byte(body), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

// run dispatches argv as u, returning the exit code and stderr.
func (f *queueFixture) run(u store.User, argv ...string) (int, string, string) {
	f.t.Helper()
	var out, errOut bytes.Buffer
	c := &Ctx{User: u, Scope: "full", Store: f.st, Stdout: &out, Stderr: &errOut}
	c.Cfg.Server.Root = f.root
	code := Dispatch(c, argv)
	return code, out.String(), errOut.String()
}

func (f *queueFixture) mustRun(u store.User, argv ...string) string {
	f.t.Helper()
	code, out, errOut := f.run(u, argv...)
	if code != protocol.ExitOK {
		f.t.Fatalf("%v: exit %d, %s", argv, code, errOut)
	}
	return out
}

func (f *queueFixture) mr() store.MR {
	f.t.Helper()
	mr, err := f.st.MRByNumber(f.repo.ID, 1)
	if err != nil {
		f.t.Fatal(err)
	}
	return mr
}

// user creates an account granted role on the repository.
func (f *queueFixture) user(name, role string) store.User {
	f.t.Helper()
	id, err := f.st.CreateUser(name, false)
	if err != nil {
		f.t.Fatal(err)
	}
	if role != "" {
		if err := f.st.GrantAccess(f.repo.ID, id, role); err != nil {
			f.t.Fatal(err)
		}
	}
	u, err := f.st.UserByID(id)
	if err != nil {
		f.t.Fatal(err)
	}
	return u
}

// systemComments is every system comment on !1, joined.
func (f *queueFixture) systemComments() string {
	f.t.Helper()
	cs, err := f.st.ListMRComments(f.mr().ID)
	if err != nil {
		f.t.Fatal(err)
	}
	var b strings.Builder
	for _, c := range cs {
		if c.Kind == "system" {
			b.WriteString(c.Body + "\n")
		}
	}
	return b.String()
}

func (f *queueFixture) wantMergedBy(who string) {
	f.t.Helper()
	mr := f.mr()
	if mr.State != "merged" || mr.MergedBy != who || mr.QueuedAt != "" {
		f.t.Fatalf("MR = state %s merged_by %q queued_at %q, want merged by %s and off the queue",
			mr.State, mr.MergedBy, mr.QueuedAt, who)
	}
	if main := strings.TrimSpace(f.git(f.dir, "rev-parse", "refs/heads/main")); main != mr.HeadSHA {
		f.t.Fatalf("main = %s, want the MR head %s", main, mr.HeadSHA)
	}
}

func (f *queueFixture) wantQueued(reason string) store.MR {
	f.t.Helper()
	mr := f.mr()
	if mr.State != "open" || mr.QueuedAt == "" || !strings.Contains(mr.QueueReason, reason) {
		f.t.Fatalf("MR = state %s queued_at %q reason %q, want open and queued with %q",
			mr.State, mr.QueuedAt, mr.QueueReason, reason)
	}
	return mr
}

// Gates that already pass merge at once: queueing is only for waiting.
func TestWhenReadyMergesAtOnceWhenGatesPass(t *testing.T) {
	f := newQueueFixture(t, nil)
	out := f.mustRun(f.alice, "mr", "merge", f.repo.Path(), "1", "--when-ready")
	if !strings.Contains(out, "merged") {
		t.Fatalf("output = %q, want it to say merged", out)
	}
	f.wantMergedBy("alice")
}

// A status that turns the checks green merges the queued request, as the
// user who queued it, and mr show carries the queue until then.
func TestWhenReadyStatusSetMerges(t *testing.T) {
	f := newQueueFixture(t, func(s *store.RepoSettings) {
		s.RequireChecks = true
		s.RequiredContexts = []string{"ext/test"}
	})
	bob := f.user("bob", "write")
	out := f.mustRun(bob, "mr", "merge", f.repo.Path(), "1", "--when-ready", "--strategy", "ff")
	if !strings.Contains(out, "queued") {
		t.Fatalf("output = %q, want it to say queued", out)
	}
	f.wantQueued("green checks")
	got := mrShowJSONAt(t, f, f.alice)
	if got.Queued == nil || got.Queued.By != "bob" || got.Queued.Strategy != "ff" || !strings.Contains(got.Queued.Reason, "green checks") {
		t.Fatalf("mr show queued = %+v", got.Queued)
	}

	f.mustRun(f.alice, "status", "set", f.repo.Path(), f.headSHA, "--context", "ext/test", "--state", "pending")
	f.wantQueued("green checks")
	f.mustRun(f.alice, "status", "set", f.repo.Path(), f.headSHA, "--context", "ext/test", "--state", "success")
	f.wantMergedBy("bob")
}

func mrShowJSONAt(t *testing.T, f *queueFixture, u store.User) mrOut {
	t.Helper()
	var out, errOut bytes.Buffer
	c := &Ctx{User: u, Scope: "full", Store: f.st, Stdout: &out, Stderr: &errOut}
	c.Cfg.Server.Root = f.root
	if code := Dispatch(c, []string{"mr", "show", f.repo.Path(), "1", "--json"}); code != protocol.ExitOK {
		t.Fatalf("mr show: exit %d, %s", code, errOut.String())
	}
	var env struct {
		Data mrOut `json:"data"`
	}
	if err := json.Unmarshal(out.Bytes(), &env); err != nil {
		t.Fatalf("mr show JSON: %v\n%s", err, out.String())
	}
	return env.Data
}

// An approval that meets require_approvals merges the queued request.
func TestWhenReadyReviewMerges(t *testing.T) {
	f := newQueueFixture(t, func(s *store.RepoSettings) { s.RequireApprovals = 1 })
	f.mustRun(f.alice, "mr", "merge", f.repo.Path(), "1", "--when-ready")
	f.wantQueued("approval")
	bob := f.user("bob", "write")
	f.mustRun(bob, "mr", "review", f.repo.Path(), "1", "--approve")
	f.wantMergedBy("alice")
}

// Resolving the last open thread merges the queued request.
func TestWhenReadyThreadResolveMerges(t *testing.T) {
	f := newQueueFixture(t, func(s *store.RepoSettings) { s.RequireResolved = true })
	id, err := f.st.AddDiffComment(f.mr().ID, f.alice.ID, f.headSHA, "feature.txt", "new", 1, "why?", 0, false)
	if err != nil {
		t.Fatal(err)
	}
	f.mustRun(f.alice, "mr", "merge", f.repo.Path(), "1", "--when-ready")
	f.wantQueued("threads resolved")
	f.mustRun(f.alice, "mr", "resolve", f.repo.Path(), "1", strconv.FormatInt(id, 10))
	f.wantMergedBy("alice")
}

// Marking a draft ready merges the queued request.
func TestWhenReadyReadyMerges(t *testing.T) {
	f := newQueueFixture(t, nil)
	f.st.SetMRDraft(f.mr().ID, true)
	f.mustRun(f.alice, "mr", "merge", f.repo.Path(), "1", "--when-ready")
	f.wantQueued("draft")
	f.mustRun(f.alice, "mr", "ready", f.repo.Path(), "1")
	f.wantMergedBy("alice")
}

// A new head keeps the request queued and has to pass on its own: a
// status on the old head moves nothing.
func TestWhenReadyNewHeadMustPassAgain(t *testing.T) {
	f := newQueueFixture(t, func(s *store.RepoSettings) {
		s.RequireChecks = true
		s.RequiredContexts = []string{"ext/test"}
	})
	f.mustRun(f.alice, "mr", "merge", f.repo.Path(), "1", "--when-ready")
	old := f.headSHA
	f.write("feature.txt", "z\n")
	f.git(f.src, "commit", "-q", "-am", "more")
	f.headSHA = strings.TrimSpace(f.git(f.src, "rev-parse", "HEAD"))
	f.git(f.src, "push", "-q", f.dir, "feature")
	f.git(f.dir, "update-ref", mrHeadRef(1), f.headSHA)
	if err := f.st.UpdateMRHead(f.mr().ID, f.headSHA, f.targetSH, false); err != nil {
		t.Fatal(err)
	}
	TryQueuedMerge(f.st, f.cfg(), f.mr().ID)
	f.wantQueued("green checks")
	f.mustRun(f.alice, "status", "set", f.repo.Path(), old, "--context", "ext/test", "--state", "success")
	f.wantQueued("green checks")
	f.mustRun(f.alice, "status", "set", f.repo.Path(), f.headSHA, "--context", "ext/test", "--state", "success")
	f.wantMergedBy("alice")
}

func (f *queueFixture) cfg() (c config.Config) {
	c.Server.Root = f.root
	return c
}

// On a require-signed repository the server cannot rebase: a branch
// behind its target stays queued and says it needs a rebase.
func TestWhenReadySignedBehindStaysQueued(t *testing.T) {
	f := newQueueFixture(t, func(s *store.RepoSettings) { s.RequireSignedCommits = true })
	f.git(f.src, "checkout", "-q", "main")
	f.write("other.txt", "o\n")
	f.git(f.src, "add", ".")
	f.git(f.src, "commit", "-q", "-m", "target moves")
	f.git(f.src, "push", "-q", f.dir, "main")
	out := f.mustRun(f.alice, "mr", "merge", f.repo.Path(), "1", "--when-ready")
	if !strings.Contains(out, "rebase") {
		t.Fatalf("output = %q, want the pending reason to name the rebase", out)
	}
	f.wantQueued("rebase")

	// A strategy that cannot ever pass there is refused, not queued.
	code, _, errOut := f.run(f.alice, "mr", "merge", f.repo.Path(), "1", "--when-ready", "--strategy", "squash")
	if code != protocol.ExitDenied || !strings.Contains(errOut, "signed") {
		t.Fatalf("squash on require-signed: exit %d, %s", code, errOut)
	}
}

// A queuer who lost write access is dequeued with the reason recorded,
// and nothing merges.
func TestWhenReadyRightsLossDequeues(t *testing.T) {
	f := newQueueFixture(t, func(s *store.RepoSettings) {
		s.RequireChecks = true
		s.RequiredContexts = []string{"ext/test"}
	})
	bob := f.user("bob", "write")
	f.mustRun(bob, "mr", "merge", f.repo.Path(), "1", "--when-ready")
	f.wantQueued("green checks")
	if err := f.st.RevokeAccess(f.repo.ID, bob.ID); err != nil {
		t.Fatal(err)
	}
	f.mustRun(f.alice, "status", "set", f.repo.Path(), f.headSHA, "--context", "ext/test", "--state", "success")
	mr := f.mr()
	if mr.State != "open" || mr.QueuedAt != "" {
		t.Fatalf("MR = state %s queued_at %q, want open and dequeued", mr.State, mr.QueuedAt)
	}
	if sys := f.systemComments(); !strings.Contains(sys, "bob no longer has write access") {
		t.Fatalf("system comments = %q, want the dequeue reason", sys)
	}
}

// --cancel dequeues; a second cancel has nothing to take off.
func TestWhenReadyCancel(t *testing.T) {
	f := newQueueFixture(t, func(s *store.RepoSettings) { s.RequireApprovals = 1 })
	f.mustRun(f.alice, "mr", "merge", f.repo.Path(), "1", "--when-ready")
	f.wantQueued("approval")
	f.mustRun(f.alice, "mr", "merge", f.repo.Path(), "1", "--cancel")
	if mr := f.mr(); mr.QueuedAt != "" {
		t.Fatalf("cancel left the MR queued: %+v", mr)
	}
	code, _, errOut := f.run(f.alice, "mr", "merge", f.repo.Path(), "1", "--cancel")
	if code != protocol.ExitFailure || !strings.Contains(errOut, "not queued") {
		t.Fatalf("second cancel: exit %d, %s", code, errOut)
	}
	code, _, _ = f.run(f.alice, "mr", "merge", f.repo.Path(), "1", "--cancel", "--when-ready")
	if code != protocol.ExitUsage {
		t.Fatalf("--cancel --when-ready: exit %d, want usage", code)
	}
	// An approval after the cancel merges nothing.
	f.mustRun(f.user("bob", "write"), "mr", "review", f.repo.Path(), "1", "--approve")
	if mr := f.mr(); mr.State != "open" {
		t.Fatalf("cancelled MR merged: %+v", mr)
	}
}

// Closing a queued merge request dequeues it.
func TestWhenReadyCloseDequeues(t *testing.T) {
	f := newQueueFixture(t, func(s *store.RepoSettings) { s.RequireApprovals = 1 })
	f.mustRun(f.alice, "mr", "merge", f.repo.Path(), "1", "--when-ready")
	f.mustRun(f.alice, "mr", "close", f.repo.Path(), "1")
	if mr := f.mr(); mr.State != "closed" || mr.QueuedAt != "" {
		t.Fatalf("closed MR = %+v, want closed and dequeued", mr)
	}
}
