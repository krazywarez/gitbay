package control

import (
	"bytes"
	"fmt"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"gitbay.org/gitbay/internal/protocol"
	"gitbay.org/gitbay/internal/store"
)

func TestSplitBuildLog(t *testing.T) {
	log := "$ git clone ssh://x/a.git (abc)\n" +
		"$ go build ./...\n" +
		"built\n" +
		"$ go test ./...\n" +
		"--- FAIL: TestX\n" +
		"step 2/2 failed: exit 1\n"
	got := SplitBuildLog(log, []string{"go build ./...", "go test ./..."})
	want := []LogSection{
		{N: 0, Text: "$ git clone ssh://x/a.git (abc)\n"},
		{N: 1, Step: "go build ./...", Text: "built\n"},
		{N: 2, Step: "go test ./...", Text: "--- FAIL: TestX\nstep 2/2 failed: exit 1\n"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}
}

// A step's line inside other output, not at a line start, does not cut;
// a build that stopped before a step has no section for it; an empty
// setup is left out.
func TestSplitBuildLogStopsAtMissingStep(t *testing.T) {
	got := SplitBuildLog("$ make\nrunning: $ make test\nerror\n", []string{"make", "make test"})
	want := []LogSection{{N: 1, Step: "make", Text: "running: $ make test\nerror\n"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}
}

func TestTailLines(t *testing.T) {
	for _, tc := range []struct {
		in   string
		n    int
		want string
	}{
		{"a\nb\nc\n", 2, "b\nc\n"},
		{"a\nb\nc\n", 5, "a\nb\nc\n"},
		{"a\nb", 1, "b"},
	} {
		if got := string(tailLines([]byte(tc.in), tc.n)); got != tc.want {
			t.Errorf("tailLines(%q, %d) = %q, want %q", tc.in, tc.n, got, tc.want)
		}
	}
}

// failedBuild is a finished failure whose second of two steps failed,
// having run 10m56s.
func failedBuild(t *testing.T) (*store.Store, store.Repo, int64, int64) {
	t.Helper()
	st, repo, uid := newQueueTestRepo(t)
	n, err := st.CreateBuild(repo.ID, "unit", "abc", "main", `["go build ./...","go test ./..."]`, "", "", true)
	if err != nil {
		t.Fatal(err)
	}
	b, ok, err := st.ClaimBuild([]int64{repo.ID}, false)
	if err != nil || !ok {
		t.Fatalf("claim: ok=%v err=%v", ok, err)
	}
	st.AppendBuildLog(b.ID, []byte("$ git clone x (abc)\n$ go build ./...\nok\n$ go test ./...\none\n--- FAIL: TestX\nstep 2/2 failed: exit 1\n"))
	if err := st.SetBuildFailure(b.ID, 2, "exit 1"); err != nil {
		t.Fatal(err)
	}
	if err := st.FinishBuild(b.ID, "failure"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB.Exec(`UPDATE builds SET started_at = '2026-09-27T10:00:00Z', finished_at = '2026-09-27T10:10:56Z' WHERE id = ?`, b.ID); err != nil {
		t.Fatal(err)
	}
	return st, repo, uid, n
}

func TestBuildLogStepAndTail(t *testing.T) {
	st, repo, uid, n := failedBuild(t)
	run := func(args ...string) (string, int) {
		t.Helper()
		c, errOut := pruneCtx(st, t.TempDir(), store.User{ID: uid})
		code := Dispatch(c, append([]string{"build", "log", repo.Path(), fmt.Sprint(n)}, args...))
		return c.Stdout.(*bytes.Buffer).String() + errOut.String(), code
	}
	if out, _ := run("--step", "1"); out != "ok\n" {
		t.Errorf("--step 1: %q", out)
	}
	if out, _ := run("--step", "failed", "--tail", "2"); out != "--- FAIL: TestX\nstep 2/2 failed: exit 1\n" {
		t.Errorf("--step failed --tail 2: %q", out)
	}
	if out, _ := run("--tail", "1"); out != "step 2/2 failed: exit 1\n" {
		t.Errorf("--tail 1: %q", out)
	}
	if _, code := run("--step", "3"); code != protocol.ExitUsage {
		t.Errorf("--step past the job: exit %d", code)
	}
	if _, code := run("--follow", "--tail", "1"); code != protocol.ExitUsage {
		t.Errorf("--follow with --tail: exit %d", code)
	}
}

func TestBuildShowNamesFailedStepAndDuration(t *testing.T) {
	st, repo, uid, n := failedBuild(t)
	c, errOut := pruneCtx(st, t.TempDir(), store.User{ID: uid})
	if code := Dispatch(c, []string{"build", "show", repo.Path(), fmt.Sprint(n)}); code != protocol.ExitOK {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	out := c.Stdout.(*bytes.Buffer).String()
	for _, re := range []string{`failed step\s+2/2 go test \./\.\.\. \(exit 1\)`, `duration\s+10m56s`} {
		if !regexp.MustCompile(re).MatchString(out) {
			t.Errorf("build show missing %s:\n%s", re, out)
		}
	}
	c, _ = pruneCtx(st, t.TempDir(), store.User{ID: uid})
	c.JSON = true
	Dispatch(c, []string{"build", "show", repo.Path(), fmt.Sprint(n)})
	for _, want := range []string{`"failed_step":2`, `"failed_reason":"exit 1"`, `"duration_s":656`, `"steps":["go build ./...","go test ./..."]`} {
		if !strings.Contains(c.Stdout.(*bytes.Buffer).String(), want) {
			t.Errorf("build show --json missing %s", want)
		}
	}
}
