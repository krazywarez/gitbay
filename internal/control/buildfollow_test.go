package control

import (
	"bytes"
	"strings"
	"sync"
	"testing"
	"time"

	"gitbay.org/gitbay/internal/protocol"
	"gitbay.org/gitbay/internal/store"
)

// syncBuffer is a bytes.Buffer guarded by a mutex, safe for a test to poll
// while the follow goroutine is still writing to it.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// follow starts build log --follow on build 1 of repo and returns the
// buffers and a channel carrying the exit code.
func follow(t *testing.T, st *store.Store, uid int64, repo store.Repo, done <-chan struct{}) (*syncBuffer, *syncBuffer, chan int) {
	t.Helper()
	return followStopping(t, st, uid, repo, done, nil)
}

// followStopping is follow on a surface that is being restarted when
// stopping closes.
func followStopping(t *testing.T, st *store.Store, uid int64, repo store.Repo, done, stopping <-chan struct{}) (*syncBuffer, *syncBuffer, chan int) {
	t.Helper()
	u, err := st.UserByID(uid)
	if err != nil {
		t.Fatal(err)
	}
	var out, errOut syncBuffer
	c := &Ctx{User: u, Scope: "full", Store: st, Stdin: strings.NewReader(""),
		Stdout: &out, Stderr: &errOut, Done: done, Stopping: stopping}
	res := make(chan int, 1)
	go func() { res <- Dispatch(c, []string{"build", "log", repo.Path(), "1", "--follow"}) }()
	return &out, &errOut, res
}

// waitOutput waits until the follower has written want, which is how a
// test knows the follow is past its first read.
func waitOutput(t *testing.T, out *syncBuffer, want string) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for !strings.Contains(out.String(), want) {
		select {
		case <-deadline:
			t.Fatalf("follow never wrote %q", want)
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func waitExit(t *testing.T, res chan int) int {
	t.Helper()
	select {
	case code := <-res:
		return code
	case <-time.After(10 * time.Second):
		t.Fatal("follow did not end")
		return -1
	}
}

func shortFollowTimers(t *testing.T) {
	settle, poll, queued, coalesce := followSettle, followPoll, followQueued, followCoalesce
	followSettle, followPoll, followCoalesce = 200*time.Millisecond, 50*time.Millisecond, 10*time.Millisecond
	t.Cleanup(func() { followSettle, followPoll, followQueued, followCoalesce = settle, poll, queued, coalesce })
}

// The follow prints the stored log, then what arrives, and ends with the
// outcome on stderr once the build finishes.
func TestBuildLogFollow(t *testing.T) {
	shortFollowTimers(t)
	st, repo, uid := newQueueTestRepo(t)
	id, err := st.CreateBuild(repo.ID, "unit", "abc", "main", `["true"]`, "", "", true)
	if err != nil {
		t.Fatal(err)
	}
	st.AppendBuildLog(id, []byte("queued\n"))
	out, errOut, res := follow(t, st, uid, repo, nil)

	if _, ok, err := st.ClaimBuild([]int64{repo.ID}, false); err != nil || !ok {
		t.Fatalf("claim: %v %v", ok, err)
	}
	st.AppendBuildLog(id, []byte("step one\n"))
	st.AppendBuildLog(id, []byte("step two\n"))
	if err := st.FinishBuild(id, "success"); err != nil {
		t.Fatal(err)
	}
	if code := waitExit(t, res); code != protocol.ExitOK {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	if got := out.String(); got != "queued\nstep one\nstep two\n" {
		t.Errorf("stdout %q", got)
	}
	if got := strings.TrimSpace(errOut.String()); got != "build 1 success" {
		t.Errorf("stderr %q", got)
	}
}

// A cancel ends the follow, and the line the cancel appends after the
// status change still arrives.
func TestBuildLogFollowCancel(t *testing.T) {
	shortFollowTimers(t)
	st, repo, uid := newQueueTestRepo(t)
	id, err := st.CreateBuild(repo.ID, "unit", "abc", "main", `["true"]`, "", "", true)
	if err != nil {
		t.Fatal(err)
	}
	out, errOut, res := follow(t, st, uid, repo, nil)
	if err := st.CancelBuild(id); err != nil {
		t.Fatal(err)
	}
	st.AppendBuildLog(id, []byte("cancelled by alice before a runner claimed it\n"))
	if code := waitExit(t, res); code != protocol.ExitOK {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	if !strings.Contains(out.String(), "cancelled by alice") {
		t.Errorf("the cancel line did not arrive: %q", out)
	}
	if got := strings.TrimSpace(errOut.String()); got != "build 1 cancelled" {
		t.Errorf("stderr %q", got)
	}
}

// Closing Done ends a follow of a build that is still running, even while
// it is blocked waiting for the next change: the build gets a line, the
// follower is confirmed to have read it (so it is back in its wait), then
// Done closes.
func TestBuildLogFollowDone(t *testing.T) {
	shortFollowTimers(t)
	st, repo, uid := newQueueTestRepo(t)
	id, err := st.CreateBuild(repo.ID, "unit", "abc", "main", `["true"]`, "", "", true)
	if err != nil {
		t.Fatal(err)
	}
	st.AppendBuildLog(id, []byte("step one\n"))
	done := make(chan struct{})
	out, errOut, res := follow(t, st, uid, repo, done)

	waitOutput(t, out, "step one")
	close(done)
	if code := waitExit(t, res); code != protocol.ExitFailure {
		t.Fatalf("exit %d, want %d", code, protocol.ExitFailure)
	}
	if errOut.String() != "" {
		t.Errorf("a follow whose reader left wrote %q", errOut)
	}
}

// A restart ends a follow and says so, so the reader knows to follow
// again.
func TestBuildLogFollowRestart(t *testing.T) {
	shortFollowTimers(t)
	st, repo, uid := newQueueTestRepo(t)
	id, err := st.CreateBuild(repo.ID, "unit", "abc", "main", `["true"]`, "", "", true)
	if err != nil {
		t.Fatal(err)
	}
	st.AppendBuildLog(id, []byte("step one\n"))
	stopping := make(chan struct{})
	out, errOut, res := followStopping(t, st, uid, repo, stopping, stopping)
	waitOutput(t, out, "step one")
	close(stopping)
	if code := waitExit(t, res); code != protocol.ExitFailure {
		t.Fatalf("exit %d, want %d", code, protocol.ExitFailure)
	}
	if got := strings.TrimSpace(errOut.String()); got != "gitbay is restarting; follow the build again in a moment" {
		t.Errorf("stderr %q", got)
	}
}

// A follower whose account is disabled mid-follow is ended, even on a
// public repository.
func TestBuildLogFollowDisabled(t *testing.T) {
	shortFollowTimers(t)
	st, repo, _ := newQueueTestRepo(t)
	bob, err := st.CreateUser("bob", false)
	if err != nil {
		t.Fatal(err)
	}
	id, err := st.CreateBuild(repo.ID, "unit", "abc", "main", `["true"]`, "", "", true)
	if err != nil {
		t.Fatal(err)
	}
	st.AppendBuildLog(id, []byte("step one\n"))
	out, errOut, res := follow(t, st, bob, repo, nil)
	waitOutput(t, out, "step one")
	if err := st.SetUserDisabled(bob, true); err != nil {
		t.Fatal(err)
	}
	if code := waitExit(t, res); code != protocol.ExitNotFound {
		t.Fatalf("exit %d, want %d: %s", code, protocol.ExitNotFound, errOut)
	}
}

// A build that stays pending ends its own follow: nothing reaps a queued
// build, so the follow must give up on its own.
func TestBuildLogFollowQueued(t *testing.T) {
	shortFollowTimers(t)
	followQueued = 150 * time.Millisecond
	st, repo, uid := newQueueTestRepo(t)
	if _, err := st.CreateBuild(repo.ID, "unit", "abc", "main", `["true"]`, "", "", true); err != nil {
		t.Fatal(err)
	}
	_, errOut, res := follow(t, st, uid, repo, nil)
	if code := waitExit(t, res); code != protocol.ExitFailure {
		t.Fatalf("exit %d, want %d: %s", code, protocol.ExitFailure, errOut)
	}
	if !strings.Contains(errOut.String(), "still queued") {
		t.Errorf("stderr %q", errOut)
	}
}

// A follower who loses read access mid-follow is ended with the answer
// a new request would get: the repository is not found.
func TestBuildLogFollowLosesAccess(t *testing.T) {
	shortFollowTimers(t)
	st, repo, _ := newQueueTestRepo(t)
	bob, err := st.CreateUser("bob", false)
	if err != nil {
		t.Fatal(err)
	}
	id, err := st.CreateBuild(repo.ID, "unit", "abc", "main", `["true"]`, "", "", true)
	if err != nil {
		t.Fatal(err)
	}
	st.AppendBuildLog(id, []byte("step one\n"))
	out, errOut, res := follow(t, st, bob, repo, nil)
	waitOutput(t, out, "step one")
	if err := st.SetRepoVisibility(repo.ID, "private"); err != nil {
		t.Fatal(err)
	}
	if code := waitExit(t, res); code != protocol.ExitNotFound {
		t.Fatalf("exit %d, want %d: %s", code, protocol.ExitNotFound, errOut)
	}
	if !strings.Contains(errOut.String(), "repository "+repo.Path()+" not found") {
		t.Errorf("stderr %q", errOut)
	}
}

// An account holding maxFollows is refused another.
func TestBuildLogFollowCap(t *testing.T) {
	st, repo, uid := newQueueTestRepo(t)
	if _, err := st.CreateBuild(repo.ID, "unit", "abc", "main", `["true"]`, "", "", true); err != nil {
		t.Fatal(err)
	}
	followMu.Lock()
	follows[uid] = maxFollows
	followMu.Unlock()
	t.Cleanup(func() {
		followMu.Lock()
		delete(follows, uid)
		followMu.Unlock()
	})
	_, errOut, res := follow(t, st, uid, repo, nil)
	if code := waitExit(t, res); code != protocol.ExitDenied {
		t.Fatalf("exit %d, want %d", code, protocol.ExitDenied)
	}
	if !strings.Contains(errOut.String(), "8 follows are already open") {
		t.Errorf("stderr %q", errOut)
	}
}
