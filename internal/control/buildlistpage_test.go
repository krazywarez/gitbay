package control

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gitbay.org/gitbay/internal/protocol"
	"gitbay.org/gitbay/internal/store"
)

// A run row led with a sha and said nothing about what the commit was
// (#241). build list carries the subject now, for every surface at once.
func TestBuildListCarriesCommitSubjects(t *testing.T) {
	st, repo, uid := newQueueTestRepo(t)
	git := gitRunner(t)
	root := t.TempDir()

	src := filepath.Join(root, "src")
	os.MkdirAll(src, 0o755)
	git(root, "init", "-q", "-b", "main", "src")
	os.WriteFile(filepath.Join(src, "a"), []byte("one\n"), 0o644)
	git(src, "add", ".")
	git(src, "commit", "-qm", "runner: cap a build's container")
	sha := strings.TrimSpace(git(src, "rev-parse", "HEAD"))

	dir := RepoDir(root, repo.OwnerName, repo.Name)
	os.MkdirAll(filepath.Dir(dir), 0o755)
	git(root, "clone", "-q", "--bare", src, dir)

	if _, err := st.CreateBuild(repo.ID, "unit", sha, "main", `["true"]`, "", "", true); err != nil {
		t.Fatal(err)
	}
	// A build whose commit is gone, as a force-push leaves behind.
	if _, err := st.CreateBuild(repo.ID, "lint", strings.Repeat("1", 40), "main", `["true"]`, "", "", true); err != nil {
		t.Fatal(err)
	}

	c, errOut := pruneCtx(st, root, store.User{ID: uid})
	c.JSON = true
	if code := Dispatch(c, []string{"build", "list", repo.Path()}); code != protocol.ExitOK {
		t.Fatalf("build list: exit %d: %s", code, errOut.String())
	}
	var got []BuildOut
	decodeData(t, c.Stdout.(*bytes.Buffer).Bytes(), &got)
	if len(got) != 2 {
		t.Fatalf("builds: %+v", got)
	}
	for _, b := range got {
		switch b.Job {
		case "unit":
			if b.Subject != "runner: cap a build's container" {
				t.Errorf("unit subject = %q", b.Subject)
			}
		case "lint":
			if b.Subject != "" {
				t.Errorf("a build whose commit is gone claims a subject: %q", b.Subject)
			}
		}
	}
}

// The builds list was capped at 50 with no way past it, so filtering to a
// sparse status reached further back and appeared to raise the total
// (#244). --limit and --cursor page it like every other list command.
func TestBuildListPages(t *testing.T) {
	st, repo, uid := newQueueTestRepo(t)
	for i := 0; i < 5; i++ {
		if _, err := st.CreateBuild(repo.ID, "unit", "aaa", "main", `["true"]`, "", "", true); err != nil {
			t.Fatal(err)
		}
	}
	c, errOut := pruneCtx(st, t.TempDir(), store.User{ID: uid})
	c.JSON = true

	page := func(args ...string) (nums []int64, next string) {
		t.Helper()
		out := c.Stdout.(*bytes.Buffer)
		out.Reset()
		errOut.Reset()
		argv := append([]string{"build", "list", repo.Path()}, args...)
		if code := Dispatch(c, argv); code != protocol.ExitOK {
			t.Fatalf("build list %v: exit %d: %s", args, code, errOut.String())
		}
		var got struct {
			Items []BuildOut `json:"items"`
			Next  string     `json:"next"`
		}
		decodeData(t, out.Bytes(), &got)
		for _, b := range got.Items {
			nums = append(nums, b.Number)
		}
		return nums, got.Next
	}

	nums, next := page("--limit", "2")
	if len(nums) != 2 || nums[0] != 5 || nums[1] != 4 {
		t.Fatalf("first page: %v", nums)
	}
	if next == "" {
		t.Fatal("first page offers no cursor with three builds left")
	}
	nums, next = page("--limit", "2", "--cursor", next)
	if len(nums) != 2 || nums[0] != 3 || nums[1] != 2 {
		t.Fatalf("second page: %v", nums)
	}
	nums, next = page("--limit", "2", "--cursor", next)
	if len(nums) != 1 || nums[0] != 1 {
		t.Fatalf("last page: %v", nums)
	}
	if next != "" {
		t.Errorf("last page offers a cursor: %q", next)
	}

	// A cursor minted by another command is not a build cursor.
	out := c.Stdout.(*bytes.Buffer)
	out.Reset()
	errOut.Reset()
	if code := Dispatch(c, []string{"build", "list", repo.Path(), "--cursor", encodeCursor("issue", "3")}); code != protocol.ExitUsage {
		t.Fatalf("foreign cursor: exit %d, want %d", code, protocol.ExitUsage)
	}
}

// decodeData unwraps the protocol envelope the JSON emitters write.
func decodeData(t *testing.T, b []byte, into any) {
	t.Helper()
	var env struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(b, &env); err != nil {
		t.Fatalf("envelope: %v\n%s", err, b)
	}
	if err := json.Unmarshal(env.Data, into); err != nil {
		t.Fatalf("data: %v\n%s", err, env.Data)
	}
}
