package control

import (
	"bytes"
	"crypto/ed25519"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gitbay.org/gitbay/internal/protocol"
	"gitbay.org/gitbay/internal/store"
	"golang.org/x/crypto/ssh"
)

// dispatchAs runs argv as u with stdin, piped, and returns stdout; a
// non-zero exit fails t.
func dispatchAs(t *testing.T, st *store.Store, u store.User, stdin string, argv ...string) string {
	t.Helper()
	return dispatchIn(t, st, u, "", stdin, argv...)
}

// dispatchIn is dispatchAs with repositories under root.
func dispatchIn(t *testing.T, st *store.Store, u store.User, root, stdin string, argv ...string) string {
	t.Helper()
	out, errOut := &bytes.Buffer{}, &bytes.Buffer{}
	c := &Ctx{User: u, Scope: "full", Store: st, Stdout: out, Stderr: errOut, Stdin: strings.NewReader(stdin)}
	c.Cfg.Server.Root = root
	if code := Dispatch(c, argv); code != protocol.ExitOK {
		t.Fatalf("%v: exit %d: %s", argv, code, errOut)
	}
	return out.String()
}

// atTerminal runs argv as u at a 100-column terminal without colour
// and returns stdout; a non-zero exit fails t.
func atTerminal(t *testing.T, st *store.Store, u store.User, argv ...string) string {
	t.Helper()
	return atTerminalIn(t, st, u, "", argv...)
}

// atTerminalIn is atTerminal with repositories under root.
func atTerminalIn(t *testing.T, st *store.Store, u store.User, root string, argv ...string) string {
	t.Helper()
	out, errOut := &bytes.Buffer{}, &bytes.Buffer{}
	c := &Ctx{User: u, Scope: "full", Store: st, Stdout: out, Stderr: errOut, Stdin: strings.NewReader(""), Term: Term{Cols: 100}}
	c.Cfg.Server.Root = root
	if code := Dispatch(c, argv); code != protocol.ExitOK {
		t.Fatalf("%v: exit %d: %s", argv, code, errOut)
	}
	return out.String()
}

// checkLegend fails t for any suggested command in a rendered screen
// (a line starting "gitbay ", or after "+n more  ") that the registry
// would not dispatch with the flags it names.
func checkLegend(t *testing.T, out string) {
	t.Helper()
	var s screen
	for _, line := range strings.Split(out, "\n") {
		if i := strings.Index(line, "more  gitbay "); i >= 0 {
			line = line[i+len("more  "):]
		}
		for _, cmd := range strings.Split(line, "  ") {
			cmd = strings.TrimSpace(cmd)
			if !strings.HasPrefix(cmd, "gitbay ") {
				continue
			}
			argv, err := protocol.Tokenize(strings.TrimPrefix(cmd, "gitbay "))
			if err != nil {
				t.Errorf("%q: %v", cmd, err)
				continue
			}
			s.actions = append(s.actions, action{"", argv})
		}
	}
	if len(s.actions) == 0 {
		t.Errorf("no suggested commands in:\n%s", out)
	}
	checkActions(t, s)
}

// newPubKey is an ed25519 public key in authorized_keys form, the same
// on every run so its fingerprint can be pinned.
func newPubKey(t *testing.T) string {
	t.Helper()
	pub := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{7}, ed25519.SeedSize)).Public().(ed25519.PublicKey)
	sp, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return string(ssh.MarshalAuthorizedKey(sp))
}

// repoListsFixture is a repository with something in every list task 1
// migrates.
func repoListsFixture(t *testing.T) (*store.Store, store.Repo, store.User) {
	t.Helper()
	st, repo, uid := newQueueTestRepo(t)
	owner := store.User{ID: uid, Username: "alice", IsAdmin: true}
	p := repo.Path()
	dispatchAs(t, st, owner, "", "repo", "topics", "add", p, "cli", "forge")
	dispatchAs(t, st, owner, "s3cret\n", "repo", "secret", "set", p, "DEPLOY_TOKEN")
	dispatchAs(t, st, owner, testRunnerPub, "repo", "deploy-key", "add", p)
	dispatchAs(t, st, owner, "", "repo", "domain", "add", p, "docs.example.test")
	dispatchAs(t, st, owner, "", "repo", "bookmark", p)
	// mirror add resolves the host, so the fixture goes to the store.
	if _, err := st.AddMirror(repo.ID, "push", "https://mirror.example.test/app.git", "", "tok"); err != nil {
		t.Fatal(err)
	}
	dispatchAs(t, st, owner, newPubKey(t), "repo", "runner", "add", p)
	return st, repo, owner
}

func TestRepoListsPlainPinned(t *testing.T) {
	st, repo, owner := repoListsFixture(t)
	p := repo.Path()
	for name, argv := range map[string][]string{
		"repo-topics":          {"repo", "topics", p},
		"repo-secret-list":     {"repo", "secret", "list", p},
		"repo-deploy-key-list": {"repo", "deploy-key", "list", p},
		"repo-domain-list":     {"repo", "domain", "list", p},
		"repo-access-list":     {"repo", "access", "list", p},
		"repo-bookmarks":       {"repo", "bookmarks"},
		"repo-search":          {"repo", "search", "app"},
		"repo-list":            {"repo", "list"},
		"repo-mirror-list":     {"repo", "mirror", "list", p},
		"repo-runner-list":     {"repo", "runner", "list", p},
	} {
		pinPlain(t, name, dispatchAs(t, st, owner, "", argv...))
	}
	pinPlain(t, "repo-topics-add", dispatchAs(t, st, owner, "", "repo", "topics", "add", p, "git"))
}

func TestRepoListScreens(t *testing.T) {
	st, repo, owner := repoListsFixture(t)
	p := repo.Path()
	for _, tc := range []struct {
		argv []string
		want []string
	}{
		{[]string{"repo", "topics", p}, []string{"Topics (2)\ncli\nforge\n"}},
		{[]string{"repo", "topics", "add", p, "git"}, []string{"Topics (3)\n"}},
		{[]string{"repo", "secret", "list", p}, []string{"Build secrets (1)\nDEPLOY_TOKEN\n"}},
		{[]string{"repo", "deploy-key", "list", p}, []string{"Deploy keys (1)\n", "  ro  runner@test  ssh-ed25519 · used never"}},
		{[]string{"repo", "domain", "list", p}, []string{"Pages domains (1)\n", "◐  docs.example.test  pending"}},
		{[]string{"repo", "access", "list", p}, []string{"Access (1)\nalice  admin  via owner\n"}},
		{[]string{"repo", "mirror", "list", p}, []string{"Mirrors (1)\n1  ◐  https://mirror.example.test/app.git  push · never synced"}},
		{[]string{"repo", "runner", "list", p}, []string{"Runners (1)\n", "  alice  ssh-ed25519 · never seen"}},
		{[]string{"repo", "bookmarks"}, []string{"Bookmarks (1)\nalice/app  public  1 bookmark\n"}},
		{[]string{"repo", "search", "app"}, []string{"Repositories matching \"app\" (1)\nalice/app  public  cli, forge"}},
		{[]string{"repo", "list"}, []string{"Repositories (1)\nalice/app  public\n"}},
	} {
		out := atTerminal(t, st, owner, tc.argv...)
		for _, w := range tc.want {
			if !strings.Contains(out, w) {
				t.Errorf("%v: missing %q in:\n%s", tc.argv, w, out)
			}
		}
		if strings.Contains(out, "s3cret") || strings.Contains(out, "tok\n") {
			t.Errorf("%v: secret on screen:\n%s", tc.argv, out)
		}
		checkLegend(t, out)
	}
}

// browseFixture is a repository on disk under root: two commits on main
// (the second touching docs/), a branch, and a tag, with fixed dates so
// the shas are the same on every run.
func browseFixture(t *testing.T) (*store.Store, store.Repo, store.User, string) {
	t.Helper()
	st, repo, uid := newQueueTestRepo(t)
	root := t.TempDir()
	env := append(gitTestEnv(), "GIT_AUTHOR_DATE=2026-09-01T10:00:00Z", "GIT_COMMITTER_DATE=2026-09-01T10:00:00Z")
	git := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir, cmd.Env = dir, env
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	src := filepath.Join(root, "src")
	if err := os.MkdirAll(filepath.Join(src, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(src, "README.md"), []byte("# app\n"), 0o644)
	git(root, "init", "-q", "-b", "main", "src")
	git(src, "add", ".")
	git(src, "commit", "-q", "-m", "first")
	os.WriteFile(filepath.Join(src, "docs", "guide.md"), []byte("# guide\n"), 0o644)
	git(src, "add", ".")
	git(src, "commit", "-q", "-m", "docs: guide")
	git(src, "tag", "v1.0.0")
	git(src, "branch", "feature")
	dir := RepoDir(root, repo.OwnerName, repo.Name)
	os.MkdirAll(filepath.Dir(dir), 0o755)
	git(root, "clone", "-q", "--bare", src, dir)
	return st, repo, store.User{ID: uid, Username: "alice"}, root
}

func TestRepoBrowsePlainPinned(t *testing.T) {
	st, repo, u, root := browseFixture(t)
	p := repo.Path()
	for name, argv := range map[string][]string{
		"repo-log":       {"repo", "log", p},
		"repo-log-path":  {"repo", "log", p, "--path", "docs/guide.md"},
		"repo-tree":      {"repo", "tree", p},
		"repo-tree-docs": {"repo", "tree", p, "docs"},
		"repo-refs":      {"repo", "refs", p},
	} {
		pinPlain(t, name, dispatchIn(t, st, u, root, "", argv...))
	}
	sst, srepo, alice, _ := symbolsFixture(t)
	pinPlain(t, "repo-symbols", dispatchAs(t, sst, alice, "", "repo", "symbols", srepo.Path(), "Pars"))
}

func TestRepoBrowseScreens(t *testing.T) {
	st, repo, u, root := browseFixture(t)
	p := repo.Path()
	for _, tc := range []struct {
		argv []string
		want []string
	}{
		{[]string{"repo", "log", p}, []string{"Commits on main (2)\n", "  docs: guide  t · "}},
		{[]string{"repo", "log", p, "--path", "docs/guide.md"}, []string{"Commits on main touching docs/guide.md (1)\n"}},
		{[]string{"repo", "tree", p}, []string{"alice/app at main (2)\n", "docs/", "README.md"}},
		{[]string{"repo", "tree", p, "docs"}, []string{"alice/app/docs at main (1)\n", "guide.md"}},
		{[]string{"repo", "refs", p}, []string{"Branches (2)\nmain", "default", "Tags (1)\nv1.0.0"}},
	} {
		out := atTerminalIn(t, st, u, root, tc.argv...)
		for _, w := range tc.want {
			if !strings.Contains(out, w) {
				t.Errorf("%v: missing %q in:\n%s", tc.argv, w, out)
			}
		}
		checkLegend(t, out)
	}
	sst, srepo, alice, _ := symbolsFixture(t)
	out := atTerminal(t, sst, alice, "repo", "symbols", srepo.Path(), "Pars")
	if !strings.Contains(out, "Symbols matching \"Pars\" (") || !strings.Contains(out, "Parse       function  p.go:1") {
		t.Errorf("symbols:\n%s", out)
	}
	checkLegend(t, out)
}
