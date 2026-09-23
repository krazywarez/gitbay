package control

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"gitbay.org/gitbay/internal/protocol"
	"gitbay.org/gitbay/internal/store"
)

// follow starts build log --follow on build 1 of repo and returns the
// buffers and a channel carrying the exit code.
func follow(t *testing.T, st *store.Store, uid int64, repo store.Repo, done <-chan struct{}) (*bytes.Buffer, *bytes.Buffer, chan int) {
	t.Helper()
	u, err := st.UserByID(uid)
	if err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	c := &Ctx{User: u, Scope: "full", Store: st, Stdin: strings.NewReader(""),
		Stdout: &out, Stderr: &errOut, Done: done}
	res := make(chan int, 1)
	go func() { res <- Dispatch(c, []string{"build", "log", repo.Path(), "1", "--follow"}) }()
	return &out, &errOut, res
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
	settle, poll := followSettle, followPoll
	followSettle, followPoll = 200*time.Millisecond, 50*time.Millisecond
	t.Cleanup(func() { followSettle, followPoll = settle, poll })
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

// Closing Done ends a follow of a build that is still running.
func TestBuildLogFollowDone(t *testing.T) {
	shortFollowTimers(t)
	st, repo, uid := newQueueTestRepo(t)
	if _, err := st.CreateBuild(repo.ID, "unit", "abc", "main", `["true"]`, "", "", true); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	_, _, res := follow(t, st, uid, repo, done)
	close(done)
	if code := waitExit(t, res); code != protocol.ExitFailure {
		t.Fatalf("exit %d, want %d", code, protocol.ExitFailure)
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
