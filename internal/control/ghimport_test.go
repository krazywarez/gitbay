package control

import (
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

	"gitbay.org/gitbay/internal/gitutil"
)

// pullUpstream serves a bare repository whose refs/pull/1/head is one
// commit over smart HTTP, and returns its port and that commit.
func pullUpstream(t *testing.T) (string, string) {
	t.Helper()
	git := gitRunner(t)
	parent := t.TempDir()
	bare := filepath.Join(parent, "o", "r.git")
	work := filepath.Join(parent, "work")
	git(parent, "init", "-q", "--bare", "--initial-branch=main", bare)
	git(parent, "init", "-q", "--initial-branch=main", work)
	git(work, "commit", "-q", "--allow-empty", "-m", "pr")
	git(work, "push", "-q", bare, "HEAD:refs/pull/1/head")
	sha := strings.TrimSpace(git(work, "rev-parse", "HEAD"))
	execPath := strings.TrimSpace(git(parent, "--exec-path"))
	srv := httptest.NewServer(&cgi.Handler{
		Path: filepath.Join(execPath, "git-http-backend"),
		Env:  []string{"GIT_PROJECT_ROOT=" + parent, "GIT_HTTP_EXPORT_ALL=1"},
	})
	t.Cleanup(srv.Close)
	u, _ := url.Parse(srv.URL)
	return u.Port(), sha
}

// stubLookup answers every host with ip and records what was asked.
func stubLookup(t *testing.T, ip string) *[]string {
	t.Helper()
	var asked []string
	prev := importLookup
	importLookup = func(ctx context.Context, host string) ([]net.IP, error) {
		asked = append(asked, host)
		return []net.IP{net.ParseIP(ip)}, nil
	}
	t.Cleanup(func() { importLookup = prev })
	return &asked
}

// pulls.test does not resolve, and the daemon's environment points git
// at a proxy and rewrites the URL to a dead port: the fetch works only
// because git was pinned to the checked address and ran with its own
// environment (#301).
func TestFetchPullHeadsIsPinned(t *testing.T) {
	port, sha := pullUpstream(t)
	t.Setenv("http_proxy", "http://127.0.0.1:1")
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	global := filepath.Join(t.TempDir(), "gitconfig")
	rewrite := "[url \"http://127.0.0.1:1/\"]\n\tinsteadOf = http://pulls.test:" + port + "/\n"
	if err := os.WriteFile(global, []byte(rewrite), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", global)
	c, errOut, _, root := importCtx(t, true)
	asked := stubLookup(t, "127.0.0.1")
	dir := filepath.Join(root, "dest.git")
	if err := gitutil.InitBare(dir, "main", ""); err != nil {
		t.Fatal(err)
	}
	if !fetchPullHeads(c, dir, "http://pulls.test:"+port+"/o/r.git", "") {
		t.Fatalf("fetch failed: %s", errOut.String())
	}
	got := strings.TrimSpace(gitRunner(t)(dir, "rev-parse", "refs/gh-pull/1"))
	if got != sha {
		t.Fatalf("refs/gh-pull/1 = %s, want %s", got, sha)
	}
	if !slices.Equal(*asked, []string{"pulls.test"}) {
		t.Fatalf("looked up %v", *asked)
	}
}

// A git host that resolves to loopback is refused on a default
// instance; the fetch reports it and fetches nothing.
func TestFetchPullHeadsRefusesALocalAddress(t *testing.T) {
	port, _ := pullUpstream(t)
	c, errOut, _, root := importCtx(t, false)
	stubLookup(t, "127.0.0.1")
	dir := filepath.Join(root, "dest.git")
	if err := gitutil.InitBare(dir, "main", ""); err != nil {
		t.Fatal(err)
	}
	if fetchPullHeads(c, dir, "http://pulls.test:"+port+"/o/r.git", "sekrit") {
		t.Fatal("fetched from a local address")
	}
	if !strings.Contains(errOut.String(), "private or local address") || strings.Contains(errOut.String(), "sekrit") {
		t.Fatalf("stderr %q", errOut.String())
	}
	if refExists(dir, "refs/gh-pull/1") {
		t.Fatal("refs/gh-pull/1 was fetched")
	}
}
