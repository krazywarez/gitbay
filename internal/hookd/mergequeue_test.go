package hookd

import (
	"bytes"
	"fmt"
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

// A fork author who cannot write to the target pushes to the source of a
// merge request someone else queued: the queue is not theirs to use, so
// it is dequeued rather than tried. Deleting the source branch dequeues
// it too.
func TestPostReceiveDequeuesQueuedMerge(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.MigrateUp(); err != nil {
		t.Fatal(err)
	}
	alice, _ := st.CreateUser("alice", false)
	bob, _ := st.CreateUser("bob", false)
	targetID, _ := st.CreateRepo("user", alice, "app", "public")
	forkID, _ := st.CreateRepo("user", bob, "app", "public")
	target, _ := st.RepoByID(targetID)
	fork, _ := st.RepoByID(forkID)
	if _, err := st.UpdateRepoSettings(target.ID, func(s *store.RepoSettings) { s.RequireApprovals = 1 }); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	f := &shapeFixture{t: t, st: st, repo: fork, uid: bob, root: root, src: filepath.Join(root, "src")}
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
	for _, r := range []store.Repo{target, fork} {
		dir := control.RepoDir(root, r.OwnerName, r.Name)
		os.MkdirAll(filepath.Dir(dir), 0o755)
		f.git(root, "init", "-q", "--bare", dir)
		f.git(f.src, "push", "-q", dir, "main", "feature")
	}
	targetDir := control.RepoDir(root, target.OwnerName, target.Name)
	f.git(targetDir, "update-ref", "refs/merge-requests/1/head", head)
	f.git(targetDir, "update-ref", "refs/merge-requests/2/head", head)
	for range 2 {
		if _, err := st.CreateMR(target.ID, bob, fork.ID, "feature", "main", "t", "", head, "md", false); err != nil {
			t.Fatal(err)
		}
	}
	for _, n := range []string{"1", "2"} {
		var out, errOut bytes.Buffer
		c := &control.Ctx{User: store.User{ID: alice, Username: "alice"}, Scope: "full", Store: st, Cfg: cfg, Stdout: &out, Stderr: &errOut}
		if code := control.Dispatch(c, []string{"mr", "merge", target.Path(), n, "--when-ready"}); code != protocol.ExitOK {
			t.Fatalf("queue !%s: exit %d, %s", n, code, errOut.String())
		}
	}
	systemSays := func(n int64, want string) {
		t.Helper()
		mr, err := st.MRByNumber(target.ID, n)
		if err != nil {
			t.Fatal(err)
		}
		if mr.QueuedAt != "" || mr.State == "merged" {
			t.Fatalf("!%d = state %s queued_at %q, want dequeued and not merged", n, mr.State, mr.QueuedAt)
		}
		cs, _ := st.ListMRComments(mr.ID)
		for _, c := range cs {
			if c.Kind == "system" && strings.Contains(c.Body, want) {
				return
			}
		}
		t.Fatalf("!%d timeline does not say %q: %+v", n, want, cs)
	}

	// !2 is closed first so the push reaches only !1.
	st.MarkClosed(mustMR(t, st, target.ID, 2).ID, alice, "")
	f.write("feature.txt", "z\n")
	f.git(f.src, "commit", "-q", "-am", "more")
	pushed := f.sha("HEAD")
	forkDir := control.RepoDir(root, fork.OwnerName, fork.Name)
	f.git(f.src, "push", "-q", forkDir, "feature")
	srv.postReceive(Request{RepoID: fork.ID, UserID: bob, Scope: "full", Updates: []policy.RefUpdate{
		{Ref: "refs/heads/feature", Old: head, New: pushed}}})
	systemSays(1, "bob pushed and cannot merge into alice/app")

	// Deleting the source dequeues a queued request.
	st.SetMRState(mustMR(t, st, target.ID, 2).ID, "open")
	var out, errOut bytes.Buffer
	c := &control.Ctx{User: store.User{ID: alice, Username: "alice"}, Scope: "full", Store: st, Cfg: cfg, Stdout: &out, Stderr: &errOut}
	if code := control.Dispatch(c, []string{"mr", "merge", target.Path(), "2", "--when-ready"}); code != protocol.ExitOK {
		t.Fatalf("queue !2: exit %d, %s", code, errOut.String())
	}
	f.git(forkDir, "update-ref", "-d", "refs/heads/feature")
	srv.postReceive(Request{RepoID: fork.ID, UserID: bob, Scope: "full", Updates: []policy.RefUpdate{
		{Ref: "refs/heads/feature", Old: pushed, New: zeroSHA40, IsDelete: true}}})
	systemSays(2, "the source branch was deleted")
}

func mustMR(t *testing.T, st *store.Store, repoID, n int64) store.MR {
	t.Helper()
	mr, err := st.MRByNumber(repoID, n)
	if err != nil {
		t.Fatal(err)
	}
	return mr
}

// A push the queue cannot attribute to a writer dequeues: one from a
// write deploy key, though the account that registered it can write, and
// one whose pusher cannot be looked up.
func TestPostReceiveDequeuesUncheckedPush(t *testing.T) {
	for _, tc := range []struct {
		name   string
		user   func(alice int64) int64
		scope  func(repoID int64) string
		reason string
	}{
		{"deploy key", func(a int64) int64 { return a }, func(id int64) string { return fmt.Sprintf("deploy:%d:rw", id) },
			"a deploy key pushed, and a deploy key cannot merge"},
		{"unknown pusher", func(int64) int64 { return 9999 }, func(int64) string { return "full" },
			"could not check who pushed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st, err := store.Open(":memory:")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { st.Close() })
			if err := st.MigrateUp(); err != nil {
				t.Fatal(err)
			}
			alice, _ := st.CreateUser("alice", false)
			repoID, _ := st.CreateRepo("user", alice, "app", "public")
			st.UpdateRepoSettings(repoID, func(s *store.RepoSettings) { s.RequireApprovals = 1 })
			repo, _ := st.RepoByID(repoID)
			root := t.TempDir()
			f := &shapeFixture{t: t, st: st, repo: repo, uid: alice, root: root, src: filepath.Join(root, "src")}
			f.dir = control.RepoDir(root, repo.OwnerName, repo.Name)
			cfg := config.Config{}
			cfg.Server.Root = root
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
			os.MkdirAll(filepath.Dir(f.dir), 0o755)
			f.git(root, "init", "-q", "--bare", f.dir)
			f.sync()
			f.git(f.dir, "update-ref", "refs/merge-requests/1/head", head)
			if _, err := st.CreateMR(repo.ID, alice, repo.ID, "feature", "main", "t", "", head, "md", false); err != nil {
				t.Fatal(err)
			}
			var out, errOut bytes.Buffer
			c := &control.Ctx{User: store.User{ID: alice, Username: "alice"}, Scope: "full", Store: st, Cfg: cfg, Stdout: &out, Stderr: &errOut}
			if code := control.Dispatch(c, []string{"mr", "merge", repo.Path(), "1", "--when-ready"}); code != protocol.ExitOK {
				t.Fatalf("queue: exit %d, %s", code, errOut.String())
			}

			f.write("feature.txt", "z\n")
			f.git(f.src, "commit", "-q", "-am", "more")
			pushed := f.sha("HEAD")
			f.sync()
			srv.postReceive(Request{RepoID: repo.ID, UserID: tc.user(alice), Scope: tc.scope(repo.ID),
				Updates: []policy.RefUpdate{{Ref: "refs/heads/feature", Old: head, New: pushed}}})

			mr := mustMR(t, st, repo.ID, 1)
			if mr.QueuedAt != "" || mr.State != "open" {
				t.Fatalf("!1 = state %s queued_at %q, want open and dequeued", mr.State, mr.QueuedAt)
			}
			cs, _ := st.ListMRComments(mr.ID)
			for _, c := range cs {
				if c.Kind == "system" && strings.Contains(c.Body, tc.reason) {
					return
				}
			}
			t.Fatalf("timeline does not say %q: %+v", tc.reason, cs)
		})
	}
}
