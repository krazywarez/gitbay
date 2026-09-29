package control

import (
	"bytes"
	"context"
	"net"
	"net/http/cgi"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"gitbay.org/gitbay/internal/protocol"
	"gitbay.org/gitbay/internal/store"
)

func importCtx(t *testing.T, allowLocal bool) (*Ctx, *bytes.Buffer, *store.Store, string) {
	t.Helper()
	st, _, uid := newQueueTestRepo(t)
	root := t.TempDir()
	c, errOut := pruneCtx(st, root, store.User{ID: uid, Username: "alice"})
	c.Cfg.Limits.WriteRate = -1
	c.Cfg.Limits.CloneTimeoutSec = 60
	c.Cfg.Webhooks.AllowLocal = allowLocal
	c.Stdin = strings.NewReader("")
	return c, errOut, st, root
}

// importUpstream serves a bare repository with one commit on main over
// smart HTTP and returns its URL and that commit.
func importUpstream(t *testing.T) (string, string) {
	t.Helper()
	git := gitRunner(t)
	parent := t.TempDir()
	bare := filepath.Join(parent, "remote.git")
	work := filepath.Join(parent, "work")
	git(parent, "init", "-q", "--bare", "--initial-branch=main", bare)
	git(parent, "init", "-q", "--initial-branch=main", work)
	git(work, "commit", "-q", "--allow-empty", "-m", "one")
	git(work, "push", "-q", bare, "main")
	sha := strings.TrimSpace(git(work, "rev-parse", "HEAD"))
	execPath := strings.TrimSpace(git(parent, "--exec-path"))
	srv := httptest.NewServer(&cgi.Handler{
		Path: filepath.Join(execPath, "git-http-backend"),
		Env:  []string{"GIT_PROJECT_ROOT=" + parent, "GIT_HTTP_EXPORT_ALL=1"},
	})
	t.Cleanup(srv.Close)
	return srv.URL + "/remote.git", sha
}

// git:// cannot be held to a checked address, so import refuses it
// before creating anything (#298).
func TestRepoImportRefusesGitScheme(t *testing.T) {
	c, errOut, st, _ := importCtx(t, true)
	code := Dispatch(c, []string{"repo", "import", "alice/x", "--from", "git://example.org/x.git"})
	if code != protocol.ExitUsage || !strings.Contains(errOut.String(), "use the repository's https:// URL") {
		t.Fatalf("exit %d, %q", code, errOut.String())
	}
	if _, err := st.RepoByPath("alice/x"); err == nil {
		t.Fatal("a refused import created a repository")
	}
}

// A source on loopback is refused on a default instance, and nothing is
// left behind.
func TestRepoImportRefusesALocalAddress(t *testing.T) {
	c, errOut, st, root := importCtx(t, false)
	code := Dispatch(c, []string{"repo", "import", "alice/x", "--from", "http://127.0.0.1:9/x.git"})
	if code != protocol.ExitFailure || !strings.Contains(errOut.String(), "127.0.0.1") {
		t.Fatalf("exit %d, %q", code, errOut.String())
	}
	if _, err := st.RepoByPath("alice/x"); err == nil {
		t.Fatal("a refused import created a repository")
	}
	if _, err := os.Stat(RepoDir(root, "alice", "x")); !os.IsNotExist(err) {
		t.Fatalf("a refused import left a directory: %v", err)
	}
}

// import.test does not resolve; the import works only because git was
// pinned to the address import looked up and checked.
func TestRepoImportConnectsToTheCheckedAddress(t *testing.T) {
	remote, sha := importUpstream(t)
	u, _ := url.Parse(remote)
	c, errOut, _, root := importCtx(t, true)
	var asked []string
	prev := importLookup
	importLookup = func(ctx context.Context, host string) ([]net.IP, error) {
		asked = append(asked, host)
		return []net.IP{net.ParseIP("127.0.0.1")}, nil
	}
	t.Cleanup(func() { importLookup = prev })

	code := Dispatch(c, []string{"repo", "import", "alice/copy", "--from", "http://import.test:" + u.Port() + "/remote.git"})
	if code != protocol.ExitOK {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	if out := c.Stdout.(*bytes.Buffer).String(); !strings.Contains(out, "default main") {
		t.Fatalf("output %q: the default branch was not read through the pin", out)
	}
	got := strings.TrimSpace(gitRunner(t)(RepoDir(root, "alice", "copy"), "rev-parse", "refs/heads/main"))
	if got != sha {
		t.Fatalf("main = %s, want %s", got, sha)
	}
	if !slices.Equal(asked, []string{"import.test"}) {
		t.Fatalf("looked up %v", asked)
	}
}
