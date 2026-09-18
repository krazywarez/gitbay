package control

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gitbay.org/gitbay/internal/config"
	"gitbay.org/gitbay/internal/protocol"
	"gitbay.org/gitbay/internal/store"
)

// prunedRepo builds a repository whose one merged MR's head is reachable
// from nothing but refs/merge-requests/1/head: main never contained it
// and the feature branch is deleted. That is the reachability a history
// rewrite leaves behind, and the only case the command is for.
func prunedRepo(t *testing.T) (*store.Store, store.Repo, string, string) {
	t.Helper()
	st, repo, uid := newQueueTestRepo(t)
	git := gitRunner(t)
	root := t.TempDir()
	src := filepath.Join(root, "src")
	os.MkdirAll(src, 0o755)
	git(root, "init", "-q", "-b", "main", "src")
	os.WriteFile(filepath.Join(src, "README"), []byte("x\n"), 0o644)
	git(src, "add", ".")
	git(src, "commit", "-q", "-m", "base")
	git(src, "checkout", "-q", "-b", "feature")
	os.WriteFile(filepath.Join(src, "README"), []byte("y\n"), 0o644)
	git(src, "add", ".")
	git(src, "commit", "-q", "-m", "change")
	headSHA := strings.TrimSpace(git(src, "rev-parse", "HEAD"))

	dir := RepoDir(root, repo.OwnerName, repo.Name)
	os.MkdirAll(filepath.Dir(dir), 0o755)
	git(root, "clone", "-q", "--bare", src, dir)
	git(dir, "symbolic-ref", "HEAD", "refs/heads/main")
	git(dir, "update-ref", mrHeadRef(1), headSHA)
	git(dir, "update-ref", "-d", "refs/heads/feature")

	if _, err := st.CreateMR(repo.ID, uid, repo.ID, "feature", "main", "t", "", headSHA, "md", false); err != nil {
		t.Fatal(err)
	}
	mr, err := st.MRByNumber(repo.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.MarkMerged(mr.ID, headSHA, uid, ""); err != nil {
		t.Fatal(err)
	}
	return st, repo, root, headSHA
}

// rootUser creates an instance admin in the store, so the system comment
// and audit row it writes have a real author.
func rootUser(t *testing.T, st *store.Store) store.User {
	t.Helper()
	id, err := st.CreateUser("root", true)
	if err != nil {
		t.Fatal(err)
	}
	return store.User{ID: id, Username: "root", IsAdmin: true}
}

func pruneCtx(st *store.Store, root string, user store.User) (*Ctx, *bytes.Buffer) {
	var errOut bytes.Buffer
	c := &Ctx{User: user, Scope: "full", Store: st, Stdout: &bytes.Buffer{}, Stderr: &errOut}
	c.Cfg.Server = config.Server{Root: root}
	return c, &errOut
}

func objectExists(dir, sha string) bool {
	cmd := exec.Command("git", "-C", dir, "cat-file", "-e", sha)
	cmd.Env = gitTestEnv()
	return cmd.Run() == nil
}

func refExists(dir, ref string) bool {
	cmd := exec.Command("git", "-C", dir, "show-ref", "--verify", "--quiet", ref)
	cmd.Env = gitTestEnv()
	return cmd.Run() == nil
}

func TestAdminMRPruneDropsHeadAndObjects(t *testing.T) {
	st, repo, root, headSHA := prunedRepo(t)
	dir := RepoDir(root, repo.OwnerName, repo.Name)
	c, errOut := pruneCtx(st, root, rootUser(t, st))

	if code := Dispatch(c, []string{"admin", "mr", "prune", repo.Path(), "1", "--yes"}); code != protocol.ExitOK {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	if refExists(dir, mrHeadRef(1)) {
		t.Error("refs/merge-requests/1/head still exists")
	}
	if objectExists(dir, headSHA) {
		t.Error("the head commit is still in the object store; gc --prune=now did not run")
	}
	mr, _ := st.MRByNumber(repo.ID, 1)
	comments, err := st.ListMRComments(mr.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(comments) != 1 || !strings.Contains(comments[0].Body, "pruned") {
		t.Errorf("want one system comment saying the head was pruned, got %+v", comments)
	}
	entries, err := st.AuditEntries(store.AuditFilter{ActionPrefix: "admin mr.prune", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("want one audit row, got %d", len(entries))
	}
}

func TestAdminMRPruneTreatsMissingRefAsDone(t *testing.T) {
	st, repo, root, _ := prunedRepo(t)
	dir := RepoDir(root, repo.OwnerName, repo.Name)
	gitRunner(t)(dir, "update-ref", "-d", mrHeadRef(1))
	c, errOut := pruneCtx(st, root, rootUser(t, st))
	if code := Dispatch(c, []string{"admin", "mr", "prune", repo.Path(), "1", "--yes"}); code != protocol.ExitOK {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
}

// Every refusal happens before any write: the ref and the objects are
// untouched afterwards, even for the number that was valid.
func TestAdminMRPruneRefusals(t *testing.T) {
	cases := []struct {
		name  string
		admin bool
		args  []string
		want  int
		msg   string
	}{
		{"non-admin", false, []string{"1", "--yes"}, protocol.ExitDenied, "instance admins"},
		{"no --yes", true, []string{"1"}, protocol.ExitUsage, "--yes"},
		{"no numbers", true, []string{"--yes"}, protocol.ExitUsage, "usage"},
		{"not a number", true, []string{"x", "--yes"}, protocol.ExitUsage, "usage"},
		{"unknown MR", true, []string{"1", "7", "--yes"}, protocol.ExitNotFound, "!7 not found"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st, repo, root, headSHA := prunedRepo(t)
			dir := RepoDir(root, repo.OwnerName, repo.Name)
			user := store.User{ID: 1, Username: "alice"}
			if tc.admin {
				user = rootUser(t, st)
			}
			c, errOut := pruneCtx(st, root, user)
			argv := append([]string{"admin", "mr", "prune", repo.Path()}, tc.args...)
			if code := Dispatch(c, argv); code != tc.want {
				t.Fatalf("exit %d, want %d: %s", code, tc.want, errOut.String())
			}
			if !strings.Contains(errOut.String(), tc.msg) {
				t.Errorf("stderr %q does not mention %q", errOut.String(), tc.msg)
			}
			if !refExists(dir, mrHeadRef(1)) || !objectExists(dir, headSHA) {
				t.Error("a refused call touched the repository")
			}
		})
	}
}

// An open or source-gone MR is still mergeable, and its head ref is what
// makes it so.
func TestAdminMRPruneRefusesMergeableMR(t *testing.T) {
	for _, state := range []string{"open", "source_gone"} {
		t.Run(state, func(t *testing.T) {
			st, repo, root, headSHA := prunedRepo(t)
			dir := RepoDir(root, repo.OwnerName, repo.Name)
			mr, _ := st.MRByNumber(repo.ID, 1)
			if err := st.SetMRState(mr.ID, state); err != nil {
				t.Fatal(err)
			}
			c, errOut := pruneCtx(st, root, rootUser(t, st))
			if code := Dispatch(c, []string{"admin", "mr", "prune", repo.Path(), "1", "--yes"}); code != protocol.ExitFailure {
				t.Fatalf("exit %d, want %d: %s", code, protocol.ExitFailure, errOut.String())
			}
			if !strings.Contains(errOut.String(), "still mergeable") {
				t.Errorf("stderr %q does not say why", errOut.String())
			}
			if !refExists(dir, mrHeadRef(1)) || !objectExists(dir, headSHA) {
				t.Error("a refused call touched the repository")
			}
		})
	}
}

// mr diff on a pruned head says so instead of leaking git's own error.
func TestMRDiffNamesAPrunedHead(t *testing.T) {
	st, repo, root, _ := prunedRepo(t)
	dir := RepoDir(root, repo.OwnerName, repo.Name)
	gitRunner(t)(dir, "update-ref", "-d", mrHeadRef(1))
	c, errOut := pruneCtx(st, root, store.User{ID: 1, Username: "alice"})
	if code := Dispatch(c, []string{"mr", "diff", repo.Path(), "1"}); code != protocol.ExitFailure {
		t.Fatalf("exit %d, want %d: %s", code, protocol.ExitFailure, errOut.String())
	}
	if !strings.Contains(errOut.String(), "no longer in the repository") || strings.Contains(errOut.String(), "exit status") {
		t.Errorf("stderr %q should say the head is gone and not echo git", errOut.String())
	}
}

// The same number twice is one MR: one deletion, one system comment.
func TestAdminMRPruneDedupesNumbers(t *testing.T) {
	st, repo, root, _ := prunedRepo(t)
	c, errOut := pruneCtx(st, root, rootUser(t, st))
	if code := Dispatch(c, []string{"admin", "mr", "prune", repo.Path(), "1", "1", "--yes"}); code != protocol.ExitOK {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	mr, _ := st.MRByNumber(repo.ID, 1)
	comments, err := st.ListMRComments(mr.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(comments) != 1 {
		t.Errorf("want one system comment, got %d", len(comments))
	}
}
