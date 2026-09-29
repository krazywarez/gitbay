package hookd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gitbay.org/gitbay/internal/config"
	"gitbay.org/gitbay/internal/control"
	"gitbay.org/gitbay/internal/policy"
	"gitbay.org/gitbay/internal/protocol"
	"gitbay.org/gitbay/internal/store"
)

// A push to the source branch of a queued merge request tries the merge
// again: a fast-forward merge queued while the branch was behind lands
// once the rebased branch is pushed.
func TestPostReceiveTriesQueuedMerge(t *testing.T) {
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
	root := t.TempDir()
	f := &shapeFixture{t: t, st: st, repo: repo, uid: uid, root: root, src: filepath.Join(root, "src")}
	f.dir = control.RepoDir(root, repo.OwnerName, repo.Name)
	cfg := config.Config{}
	cfg.Server.Root, cfg.Server.SiteURL = root, "https://x.test"
	srv := &Server{cfg: cfg, st: st}

	os.MkdirAll(f.src, 0o755)
	f.git(root, "init", "-q", "-b", "main", "src")
	f.write("README", "x\n")
	f.git(f.src, "add", ".")
	f.git(f.src, "commit", "-q", "-m", "base")
	f.git(f.src, "checkout", "-q", "-b", "feature")
	f.write("feature.txt", "y\n")
	f.git(f.src, "add", ".")
	f.git(f.src, "commit", "-q", "-m", "change")
	head := f.sha("HEAD")
	f.git(f.src, "checkout", "-q", "main")
	f.write("other.txt", "o\n")
	f.git(f.src, "add", ".")
	f.git(f.src, "commit", "-q", "-m", "target moves")
	os.MkdirAll(filepath.Dir(f.dir), 0o755)
	f.git(root, "init", "-q", "--bare", f.dir)
	f.sync()
	f.git(f.dir, "update-ref", "refs/merge-requests/1/head", head)
	if _, err := st.CreateMR(repo.ID, uid, repo.ID, "feature", "main", "t", "", head, "md", false); err != nil {
		t.Fatal(err)
	}

	var out, errOut bytes.Buffer
	c := &control.Ctx{User: store.User{ID: uid, Username: "alice"}, Scope: "full", Store: st, Cfg: cfg, Stdout: &out, Stderr: &errOut}
	if code := control.Dispatch(c, []string{"mr", "merge", repo.Path(), "1", "--when-ready", "--strategy", "ff"}); code != protocol.ExitOK {
		t.Fatalf("mr merge --when-ready: exit %d, %s", code, errOut.String())
	}
	if mr, _ := st.MRByNumber(repo.ID, 1); mr.QueuedAt == "" || !strings.Contains(mr.QueueReason, "fast-forward not possible") {
		t.Fatalf("queued MR = %+v, want it waiting on a fast-forward", mr)
	}

	f.git(f.src, "checkout", "-q", "feature")
	f.git(f.src, "rebase", "-q", "main")
	rebased := f.sha("HEAD")
	f.sync()
	srv.postReceive(Request{RepoID: repo.ID, UserID: uid, Scope: "full", Updates: []policy.RefUpdate{
		{Ref: "refs/heads/feature", Old: head, New: rebased, IsForce: true}}})

	mr, err := st.MRByNumber(repo.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if mr.State != "merged" || mr.MergedBy != "alice" || mr.QueuedAt != "" {
		t.Fatalf("MR after push = %s by %q queued %q (%s), want merged by alice", mr.State, mr.MergedBy, mr.QueuedAt, mr.QueueReason)
	}
	if main := strings.TrimSpace(f.git(f.dir, "rev-parse", "refs/heads/main")); main != rebased {
		t.Fatalf("main = %s, want %s", main, rebased)
	}
}
