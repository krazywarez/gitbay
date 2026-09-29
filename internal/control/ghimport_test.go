package control

import (
	"context"
	"net"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"gitbay.org/gitbay/internal/gitutil"
	"gitbay.org/gitbay/internal/protocol"
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

// apiServer serves an empty issue list for o/r, plus whatever extra
// registers, and returns the server's port.
func apiServer(t *testing.T, extra func(mux *http.ServeMux)) string {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/o/r/issues", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("[]"))
	})
	if extra != nil {
		extra(mux)
	}
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	u, _ := url.Parse(srv.URL)
	return u.Port()
}

// api.test does not resolve; the import reaches the API only because the
// client dialed the address import-issues looked up and checked (#301).
func TestImportIssuesConnectsToTheCheckedAddress(t *testing.T) {
	port := apiServer(t, nil)
	c, errOut, _, _ := importCtx(t, true)
	asked := stubLookup(t, "127.0.0.1")
	code := Dispatch(c, []string{"repo", "import-issues", "alice/app", "--from", "o/r", "--api-base", "http://api.test:" + port})
	if code != protocol.ExitOK {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	if len(*asked) == 0 || (*asked)[0] != "api.test" {
		t.Fatalf("looked up %v", *asked)
	}
}

// A redirect is refused and its target never asked, although the dialer
// would land it on the checked address.
func TestImportIssuesRefusesARedirect(t *testing.T) {
	var port string
	reached := false
	port = apiServer(t, func(mux *http.ServeMux) {
		mux.HandleFunc("/repos/o/x/issues", func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "http://other.test:"+port+"/repos/o/x/moved", http.StatusMovedPermanently)
		})
		mux.HandleFunc("/repos/o/x/moved", func(w http.ResponseWriter, r *http.Request) {
			reached = true
			w.Write([]byte("[]"))
		})
	})
	c, errOut, _, _ := importCtx(t, true)
	stubLookup(t, "127.0.0.1")
	code := Dispatch(c, []string{"repo", "import-issues", "alice/app", "--from", "o/x", "--api-base", "http://api.test:" + port})
	if code != protocol.ExitFailure || !strings.Contains(errOut.String(), "refusing redirect to http://other.test") {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	if reached {
		t.Fatal("followed a redirect to another host")
	}
}

// An API base that resolves to private space is refused on a default
// instance before anything connects.
func TestImportIssuesRefusesAPrivateAPIBase(t *testing.T) {
	c, errOut, _, _ := importCtx(t, false)
	asked := stubLookup(t, "10.0.0.1")
	code := Dispatch(c, []string{"repo", "import-issues", "alice/app", "--from", "o/r", "--api-base", "https://api.test"})
	if code != protocol.ExitUsage || !strings.Contains(errOut.String(), "private or local address") {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	if !slices.Equal(*asked, []string{"api.test"}) {
		t.Fatalf("looked up %v", *asked)
	}
}

// --api-base takes a plain http or https URL: no credentials, query,
// fragment or other scheme, and nothing of a refused one is echoed.
func TestImportIssuesRefusesAnAPIBaseShape(t *testing.T) {
	for _, base := range []string{"https://abc@api.test", "https://api.test/?abc", "https://api.test/#abc", "ftp://api.test/abc"} {
		c, errOut, _, _ := importCtx(t, true)
		asked := stubLookup(t, "127.0.0.1")
		code := Dispatch(c, []string{"repo", "import-issues", "alice/app", "--from", "o/r", "--api-base", base})
		if code != protocol.ExitUsage || len(*asked) != 0 || strings.Contains(errOut.String(), "abc") {
			t.Fatalf("%s: exit %d, looked up %v: %s", base, code, *asked, errOut.String())
		}
	}
}
