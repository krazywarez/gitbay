package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"

	"gitbay.org/gitbay/internal/hookd"
	"gitbay.org/gitbay/internal/policy"
)

// fastImportRepo builds a repository with n commits on main in one
// process. A loop of `git commit` would be n forks, which is the cost this
// test exists to rule out of the code under test.
func fastImportRepo(t *testing.T, n int) (string, string) {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	run("init", "-q", "--initial-branch=main", ".")

	var b strings.Builder
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, "commit refs/heads/main\n")
		fmt.Fprintf(&b, "mark :%d\n", i)
		fmt.Fprintf(&b, "author A U Thor <a@example.test> %d +0000\n", 1600000000+i)
		fmt.Fprintf(&b, "committer A U Thor <a@example.test> %d +0000\n", 1600000000+i)
		msg := fmt.Sprintf("commit %d", i)
		fmt.Fprintf(&b, "data %d\n%s\n", len(msg), msg)
		if i > 1 {
			fmt.Fprintf(&b, "from :%d\n", i-1)
		}
		fmt.Fprintf(&b, "M 644 inline f.txt\ndata %d\n%d\n", len(fmt.Sprint(i))+1, i)
		fmt.Fprintf(&b, "\n")
	}
	cmd := exec.Command("git", "fast-import", "--quiet")
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(b.String())
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("fast-import: %v\n%s", err, out)
	}
	// pre-receive runs before the ref moves, so the incoming objects are
	// present but reachable from nothing — which is what makes
	// `rev-list --not --all` list them. Dropping the ref reproduces that;
	// the objects stay until a gc that never runs here.
	head := gitOut(t, dir, "rev-parse", "main")
	run("update-ref", "-d", "refs/heads/main")
	return dir, head
}

// streamIncomingCommits runs in the hook process, whose working directory
// is the repository, so the test chdirs the same way git would.
func inRepo(t *testing.T, dir string, f func()) {
	t.Helper()
	prev, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(prev)
	f()
}

// A push large enough that its object names do not fit a pipe buffer:
// writing them all before reading git's output deadlocks, which is why
// stdin is fed from its own goroutine. 4000 names is roughly 160 KiB,
// well past the usual 64 KiB pipe.
func TestStreamIncomingCommitsLargePush(t *testing.T) {
	const n = 4000
	dir, head := fastImportRepo(t, n)

	// No ref exists, so every commit is incoming — the shape of a
	// first push of an existing history.
	updates := []policy.RefUpdate{{Old: strings.Repeat("0", 40), New: head, Ref: "refs/heads/main"}}

	var got []hookd.RawCommit
	inRepo(t, dir, func() {
		if err := streamIncomingCommits(updates, func(rc hookd.RawCommit) error {
			got = append(got, rc)
			return nil
		}); err != nil {
			t.Fatalf("streaming: %v", err)
		}
	})
	if len(got) != n {
		t.Fatalf("streamed %d commits, want %d", len(got), n)
	}
	seen := map[string]bool{}
	for _, rc := range got {
		if seen[rc.SHA] {
			t.Fatalf("%s streamed twice", rc.SHA)
		}
		seen[rc.SHA] = true
		// The raw object is what signature verification parses; a record
		// misread by a byte would still look plausible here without this.
		if !strings.HasPrefix(string(rc.Raw), "tree ") {
			t.Fatalf("%s does not look like a raw commit: %.60q", rc.SHA, rc.Raw)
		}
		if !strings.Contains(string(rc.Raw), "A U Thor <a@example.test>") {
			t.Fatalf("%s raw object is truncated: %.200q", rc.SHA, rc.Raw)
		}
	}
	if !seen[head] {
		t.Fatal("the pushed tip was not among the streamed commits")
	}
}

// A ref that already exists contributes nothing, and a delete contributes
// nothing: neither introduces an object to verify.
func TestStreamIncomingCommitsNothingToDo(t *testing.T) {
	dir, head := fastImportRepo(t, 3)
	zero := strings.Repeat("0", 40)

	inRepo(t, dir, func() {
		n := 0
		err := streamIncomingCommits([]policy.RefUpdate{
			{Old: head, New: zero, Ref: "refs/heads/main", IsDelete: true},
		}, func(hookd.RawCommit) error { n++; return nil })
		if err != nil || n != 0 {
			t.Fatalf("delete streamed %d commits (%v)", n, err)
		}
	})

	// With the ref restored the tip is reachable, so nothing is incoming:
	// a push of what the repository already has verifies nothing.
	cmd := exec.Command("git", "update-ref", "refs/heads/main", head)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("update-ref: %v\n%s", err, out)
	}
	inRepo(t, dir, func() {
		n := 0
		err := streamIncomingCommits([]policy.RefUpdate{
			{Old: head, New: head, Ref: "refs/heads/main"},
		}, func(hookd.RawCommit) error { n++; return nil })
		if err != nil || n != 0 {
			t.Fatalf("already-present tip streamed %d commits (%v)", n, err)
		}
	})
}

// An error from the callback — the socket going away mid-push — stops the
// walk instead of reading the rest of the history into nothing.
func TestStreamIncomingCommitsCallbackError(t *testing.T) {
	// Large enough that git is still writing when the callback gives up:
	// without killing it, git blocks on a full pipe and Wait blocks on
	// git, and this test hangs rather than fails.
	dir, head := fastImportRepo(t, 4000)
	inRepo(t, dir, func() {
		n := 0
		err := streamIncomingCommits(
			[]policy.RefUpdate{{Old: strings.Repeat("0", 40), New: head, Ref: "refs/heads/main"}},
			func(hookd.RawCommit) error {
				n++
				if n == 5 {
					return fmt.Errorf("socket closed")
				}
				return nil
			})
		if err == nil {
			t.Fatal("callback error did not stop the walk")
		}
		if n != 5 {
			t.Fatalf("kept streaming after the error: %d commits", n)
		}
	})
}

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %s: %v", strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(out))
}
