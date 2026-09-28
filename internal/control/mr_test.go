package control

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"gitbay.org/gitbay/internal/protocol"
	"gitbay.org/gitbay/internal/store"
)

// mrTestCtx runs control commands as owner against st, capturing output.
func mrTestCtx(st *store.Store, owner store.User) (*Ctx, *bytes.Buffer, *bytes.Buffer) {
	out, errOut := &bytes.Buffer{}, &bytes.Buffer{}
	c := &Ctx{User: owner, Scope: "full", Store: st, Stdout: out, Stderr: errOut}
	return c, out, errOut
}

// twoMRTestRepo is a repository with two open merge requests, both
// authored by the returned owner, so authorOrWrite never gets in the way.
func twoMRTestRepo(t *testing.T) (*store.Store, store.Repo, store.User) {
	t.Helper()
	st, repo, uid := newQueueTestRepo(t)
	owner := store.User{ID: uid, Username: "alice"}
	if _, err := st.CreateMR(repo.ID, uid, repo.ID, "feature1", "main", "one", "", "abc111", "md", false); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateMR(repo.ID, uid, repo.ID, "feature2", "main", "two", "", "abc222", "md", false); err != nil {
		t.Fatal(err)
	}
	return st, repo, owner
}

func mrShowJSON(t *testing.T, st *store.Store, owner store.User, path string, n int64) mrOut {
	t.Helper()
	c, out, errOut := mrTestCtx(st, owner)
	if code := Dispatch(c, []string{"mr", "show", path, strconv.FormatInt(n, 10), "--json"}); code != protocol.ExitOK {
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

// eventDataFor pulls the most recent data_json for a kind, so a test can
// check what mr close recorded without a store accessor built just for it.
func eventDataFor(t *testing.T, st *store.Store, kind string) string {
	t.Helper()
	var data string
	err := st.DB.QueryRow("SELECT data_json FROM events WHERE kind = ? ORDER BY id DESC LIMIT 1", kind).Scan(&data)
	if err != nil {
		t.Fatalf("event %s: %v", kind, err)
	}
	return data
}

// Closing a merge request can name the one that carries its change
// forward; mr show and the mr.closed event both then carry it (#223).
func TestMRCloseWithBy(t *testing.T) {
	st, repo, owner := twoMRTestRepo(t)
	c, _, errOut := mrTestCtx(st, owner)
	if code := Dispatch(c, []string{"mr", "close", repo.Path(), "1", "--by", "2"}); code != protocol.ExitOK {
		t.Fatalf("mr close: exit %d, %s", code, errOut.String())
	}
	got := mrShowJSON(t, st, owner, repo.Path(), 1)
	if got.State != "closed" || got.SupersededBy != 2 {
		t.Fatalf("mr show !1 = %+v, want closed superseded_by 2", got)
	}
	if data := eventDataFor(t, st, "mr.closed"); !strings.Contains(data, `"by":2`) {
		t.Fatalf("mr.closed event = %s, want it to carry by:2", data)
	}
}

// mr close --by refuses a merge request naming itself.
func TestMRCloseBySelfRefused(t *testing.T) {
	st, repo, owner := twoMRTestRepo(t)
	c, _, errOut := mrTestCtx(st, owner)
	code := Dispatch(c, []string{"mr", "close", repo.Path(), "1", "--by", "1"})
	if code != protocol.ExitUsage {
		t.Fatalf("exit = %d, want %d; stderr: %s", code, protocol.ExitUsage, errOut.String())
	}
	if !strings.Contains(errOut.String(), "cannot supersede itself") {
		t.Fatalf("stderr = %q, want it to say a merge request cannot supersede itself", errOut.String())
	}
}

// mr close --by refuses a merge request number that does not exist in
// the repository.
func TestMRCloseByMissingRefused(t *testing.T) {
	st, repo, owner := twoMRTestRepo(t)
	c, _, errOut := mrTestCtx(st, owner)
	code := Dispatch(c, []string{"mr", "close", repo.Path(), "1", "--by", "99"})
	if code != protocol.ExitNotFound {
		t.Fatalf("exit = %d, want %d; stderr: %s", code, protocol.ExitNotFound, errOut.String())
	}
	if !strings.Contains(errOut.String(), "no merge request !99") {
		t.Fatalf("stderr = %q, want it to name !99 as missing", errOut.String())
	}
}

// mr edit --superseded-by sets and clears the field on a closed merge
// request.
func TestMREditSupersededBySetAndClear(t *testing.T) {
	st, repo, owner := twoMRTestRepo(t)
	c, _, errOut := mrTestCtx(st, owner)
	if code := Dispatch(c, []string{"mr", "close", repo.Path(), "1"}); code != protocol.ExitOK {
		t.Fatalf("mr close: exit %d, %s", code, errOut.String())
	}
	c, _, errOut = mrTestCtx(st, owner)
	if code := Dispatch(c, []string{"mr", "edit", repo.Path(), "1", "--superseded-by", "2"}); code != protocol.ExitOK {
		t.Fatalf("mr edit --superseded-by 2: exit %d, %s", code, errOut.String())
	}
	if got := mrShowJSON(t, st, owner, repo.Path(), 1); got.SupersededBy != 2 {
		t.Fatalf("SupersededBy = %d, want 2", got.SupersededBy)
	}
	c, _, errOut = mrTestCtx(st, owner)
	if code := Dispatch(c, []string{"mr", "edit", repo.Path(), "1", "--superseded-by", "none"}); code != protocol.ExitOK {
		t.Fatalf("mr edit --superseded-by none: exit %d, %s", code, errOut.String())
	}
	if got := mrShowJSON(t, st, owner, repo.Path(), 1); got.SupersededBy != 0 {
		t.Fatalf("SupersededBy after clear = %d, want 0", got.SupersededBy)
	}
}

// mr edit --superseded-by refuses a self-reference the same way mr close
// --by does.
func TestMREditSupersededBySelfRefused(t *testing.T) {
	st, repo, owner := twoMRTestRepo(t)
	c, _, errOut := mrTestCtx(st, owner)
	if code := Dispatch(c, []string{"mr", "close", repo.Path(), "1"}); code != protocol.ExitOK {
		t.Fatalf("mr close: exit %d, %s", code, errOut.String())
	}
	c, _, errOut = mrTestCtx(st, owner)
	code := Dispatch(c, []string{"mr", "edit", repo.Path(), "1", "--superseded-by", "1"})
	if code != protocol.ExitUsage {
		t.Fatalf("exit = %d, want %d; stderr: %s", code, protocol.ExitUsage, errOut.String())
	}
	if !strings.Contains(errOut.String(), "cannot supersede itself") {
		t.Fatalf("stderr = %q, want it to say a merge request cannot supersede itself", errOut.String())
	}
}

// mr edit --superseded-by refuses a merge request number that does not
// exist in the repository.
func TestMREditSupersededByMissingRefused(t *testing.T) {
	st, repo, owner := twoMRTestRepo(t)
	c, _, errOut := mrTestCtx(st, owner)
	if code := Dispatch(c, []string{"mr", "close", repo.Path(), "1"}); code != protocol.ExitOK {
		t.Fatalf("mr close: exit %d, %s", code, errOut.String())
	}
	c, _, errOut = mrTestCtx(st, owner)
	code := Dispatch(c, []string{"mr", "edit", repo.Path(), "1", "--superseded-by", "99"})
	if code != protocol.ExitNotFound {
		t.Fatalf("exit = %d, want %d; stderr: %s", code, protocol.ExitNotFound, errOut.String())
	}
	if !strings.Contains(errOut.String(), "no merge request !99") {
		t.Fatalf("stderr = %q, want it to name !99 as missing", errOut.String())
	}
}

// mr edit --superseded-by refuses an open merge request: only a closed
// one can be superseded.
func TestMREditSupersededByOnOpenMRRefused(t *testing.T) {
	st, repo, owner := twoMRTestRepo(t)
	c, _, errOut := mrTestCtx(st, owner)
	code := Dispatch(c, []string{"mr", "edit", repo.Path(), "1", "--superseded-by", "2"})
	if code != protocol.ExitUsage {
		t.Fatalf("exit = %d, want %d; stderr: %s", code, protocol.ExitUsage, errOut.String())
	}
	if !strings.Contains(errOut.String(), "only a closed merge request can be superseded") {
		t.Fatalf("stderr = %q, want the closed-only refusal", errOut.String())
	}
}

// mr show pluralizes multi-row section headings with counts.
func TestMRShowPluralizesMultiRowSections(t *testing.T) {
	st, repo, uid := newQueueTestRepo(t)
	owner := store.User{ID: uid, Username: "alice"}

	// Create git commits for the MR
	git := gitRunner(t)
	root := t.TempDir()

	// Create a temporary repository to set up commits
	src := filepath.Join(root, "src")
	os.MkdirAll(src, 0o755)
	git(root, "init", "-q", "-b", "main", "src")

	// Create base commit on main
	os.WriteFile(filepath.Join(src, "file.txt"), []byte("content"), 0o644)
	git(src, "add", ".")
	git(src, "commit", "-q", "-m", "initial")

	// Create feature branch with 2 commits
	git(src, "checkout", "-q", "-b", "feature")
	os.WriteFile(filepath.Join(src, "file.txt"), []byte("content1"), 0o644)
	git(src, "add", ".")
	git(src, "commit", "-q", "-m", "commit1")

	os.WriteFile(filepath.Join(src, "file.txt"), []byte("content2"), 0o644)
	git(src, "add", ".")
	git(src, "commit", "-q", "-m", "commit2")
	headSHA := strings.TrimSpace(git(src, "rev-parse", "HEAD"))

	// Clone as a bare repository to the gitbay path
	dir := RepoDir(root, repo.OwnerName, repo.Name)
	os.MkdirAll(filepath.Dir(dir), 0o755)
	git(root, "clone", "-q", "--bare", "src", dir)

	// Create the MR head ref in the bare repository
	git(dir, "update-ref", "refs/merge-requests/1/head", headSHA)

	// Create an MR
	_, err := st.CreateMR(repo.ID, uid, repo.ID, "feature", "main", "Feature", "", headSHA, "md", false)
	if err != nil {
		t.Fatalf("CreateMR: %v", err)
	}

	// Add 2 checks
	if err := st.SetCommitStatus(repo.ID, headSHA, "check1", "success", "", "", 0); err != nil {
		t.Fatal(err)
	}
	if err := st.SetCommitStatus(repo.ID, headSHA, "check2", "success", "", "", 0); err != nil {
		t.Fatal(err)
	}

	// Add 2 reviews
	bob, err := st.CreateUser("bob", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.AddMRReview(1, bob, "approve", headSHA); err != nil {
		t.Fatal(err)
	}
	charlie, err := st.CreateUser("charlie", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.AddMRReview(1, charlie, "approve", headSHA); err != nil {
		t.Fatal(err)
	}

	// Call mr show in plain text mode
	c, out, errOut := mrTestCtx(st, owner)
	c.Cfg.Server.Root = root
	if code := Dispatch(c, []string{"mr", "show", repo.Path(), "1"}); code != protocol.ExitOK {
		t.Fatalf("mr show: exit %d, %s", code, errOut.String())
	}

	outStr := out.String()
	for _, want := range []string{"commits (2):", "checks (2):", "reviews (2):"} {
		if !strings.Contains(outStr, want) {
			t.Errorf("missing %q in:\n%s", want, outStr)
		}
	}
}
