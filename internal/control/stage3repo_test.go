package control

import (
	"bytes"
	"crypto/ed25519"
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
	out, errOut := &bytes.Buffer{}, &bytes.Buffer{}
	c := &Ctx{User: u, Scope: "full", Store: st, Stdout: out, Stderr: errOut, Stdin: strings.NewReader(stdin)}
	if code := Dispatch(c, argv); code != protocol.ExitOK {
		t.Fatalf("%v: exit %d: %s", argv, code, errOut)
	}
	return out.String()
}

// atTerminal runs argv as u at a 100-column terminal without colour
// and returns stdout; a non-zero exit fails t.
func atTerminal(t *testing.T, st *store.Store, u store.User, argv ...string) string {
	t.Helper()
	out, errOut := &bytes.Buffer{}, &bytes.Buffer{}
	c := &Ctx{User: u, Scope: "full", Store: st, Stdout: out, Stderr: errOut, Stdin: strings.NewReader(""), Term: Term{Cols: 100}}
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
