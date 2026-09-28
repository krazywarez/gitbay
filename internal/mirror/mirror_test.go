package mirror

import (
	"context"
	"net"
	"net/http/cgi"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"gitbay.org/gitbay/internal/config"
	"gitbay.org/gitbay/internal/control"
	"gitbay.org/gitbay/internal/store"
)

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "HOME="+t.TempDir(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.test",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.test")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// upstream serves a bare repository with one commit on main over smart
// HTTP and returns its URL and that commit.
func upstream(t *testing.T) (string, string) {
	t.Helper()
	parent := t.TempDir()
	bare := filepath.Join(parent, "remote.git")
	work := filepath.Join(parent, "work")
	git(t, parent, "init", "-q", "--bare", "--initial-branch=main", bare)
	git(t, parent, "init", "-q", "--initial-branch=main", work)
	git(t, work, "commit", "-q", "--allow-empty", "-m", "one")
	git(t, work, "push", "-q", bare, "main")
	sha := git(t, work, "rev-parse", "HEAD")
	execPath := git(t, parent, "--exec-path")
	srv := httptest.NewServer(&cgi.Handler{
		Path: filepath.Join(execPath, "git-http-backend"),
		Env:  []string{"GIT_PROJECT_ROOT=" + parent, "GIT_HTTP_EXPORT_ALL=1"},
	})
	t.Cleanup(srv.Close)
	return srv.URL + "/remote.git", sha
}

// local returns a store with alice/app, its bare repository under root,
// and the pull mirror row for url.
func local(t *testing.T, root, mirrorURL string) (*store.Store, store.Mirror, string) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "gitbay.db"))
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
	dir := control.RepoDir(root, "alice", "app")
	os.MkdirAll(filepath.Dir(dir), 0o755)
	git(t, root, "init", "-q", "--bare", dir)
	if _, err := st.AddMirror(repoID, "pull", mirrorURL, "", ""); err != nil {
		t.Fatal(err)
	}
	due, err := st.DueMirrors(900)
	if err != nil || len(due) != 1 {
		t.Fatalf("due mirrors: %v %v", due, err)
	}
	return st, due[0], dir
}

// mirror.test does not resolve; the fetch works only because git was
// pinned to the address the worker looked up and checked.
func TestSyncConnectsToTheCheckedAddress(t *testing.T) {
	remote, sha := upstream(t)
	u, _ := url.Parse(remote)
	root := t.TempDir()
	st, m, dir := local(t, root, "http://mirror.test:"+u.Port()+"/remote.git")
	var cfg config.Config
	cfg.Server.Root = root
	cfg.Webhooks.AllowLocal = true
	var asked []string
	w := &Worker{St: st, Cfg: cfg, Lookup: func(ctx context.Context, host string) ([]net.IP, error) {
		asked = append(asked, host)
		return []net.IP{net.ParseIP("127.0.0.1")}, nil
	}}
	if err := w.sync(m); err != nil {
		t.Fatal(err)
	}
	if got := git(t, dir, "rev-parse", "refs/heads/main"); got != sha {
		t.Fatalf("main = %s, want %s", got, sha)
	}
	if !slices.Equal(asked, []string{"mirror.test"}) {
		t.Fatalf("looked up %v", asked)
	}
}

// The URL passed the check when it was saved; the answer at sync time
// is what counts.
func TestSyncRefusesAPrivateAddressAtSyncTime(t *testing.T) {
	root := t.TempDir()
	st, m, _ := local(t, root, "https://mirror.test/x.git")
	var cfg config.Config
	cfg.Server.Root = root
	w := &Worker{St: st, Cfg: cfg, Lookup: func(context.Context, string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("10.0.0.7")}, nil
	}}
	err := w.sync(m)
	if err == nil || !strings.Contains(err.Error(), "10.0.0.7") {
		t.Fatalf("sync = %v, want a refusal naming 10.0.0.7", err)
	}
}

// A refusal is a sync failure like any other: the sweep records it on
// the mirror, where repo mirror list shows it.
func TestSweepRecordsTheRefusal(t *testing.T) {
	root := t.TempDir()
	st, m, _ := local(t, root, "https://mirror.test/x.git")
	var cfg config.Config
	cfg.Server.Root = root
	cfg.Mirrors.PullIntervalMinutes = 15
	w := &Worker{St: st, Cfg: cfg, Lookup: func(context.Context, string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("100.64.0.9")}, nil
	}}
	w.sweep()
	ms, err := st.ListMirrors(m.RepoID)
	if err != nil || len(ms) != 1 {
		t.Fatalf("mirrors: %v %v", ms, err)
	}
	if !strings.Contains(ms[0].LastError, "100.64.0.9") {
		t.Fatalf("last error = %q", ms[0].LastError)
	}
}

func TestSyncRefusesAnEmptyAnswer(t *testing.T) {
	root := t.TempDir()
	st, m, _ := local(t, root, "https://mirror.test/x.git")
	var cfg config.Config
	cfg.Server.Root = root
	cfg.Webhooks.AllowLocal = true
	w := &Worker{St: st, Cfg: cfg, Lookup: func(context.Context, string) ([]net.IP, error) {
		return nil, nil
	}}
	if err := w.sync(m); err == nil || !strings.Contains(err.Error(), "no address") {
		t.Fatalf("sync = %v, want a refusal", err)
	}
}

func TestSyncRefusesANonHTTPScheme(t *testing.T) {
	root := t.TempDir()
	st, m, _ := local(t, root, "ssh://mirror.test/x.git")
	var cfg config.Config
	cfg.Server.Root = root
	cfg.Webhooks.AllowLocal = true
	w := &Worker{St: st, Cfg: cfg, Lookup: func(context.Context, string) ([]net.IP, error) {
		t.Fatal("looked up a host for an ssh URL")
		return nil, nil
	}}
	if err := w.sync(m); err == nil || !strings.Contains(err.Error(), "not http or https") {
		t.Fatalf("sync = %v, want a refusal", err)
	}
}

func TestPinArgs(t *testing.T) {
	u, _ := url.Parse("https://git.example/x.git")
	got := pinArgs(u, []net.IP{net.ParseIP("203.0.113.5"), net.ParseIP("2001:db8::1")})
	want := []string{"-c", "http.followRedirects=false",
		"-c", "http.curloptResolve=git.example:443:203.0.113.5,[2001:db8::1]"}
	if !slices.Equal(got, want) {
		t.Fatalf("https: %q", got)
	}
	u, _ = url.Parse("http://git.example:8080/x.git")
	if got := pinArgs(u, []net.IP{net.ParseIP("203.0.113.5")}); got[3] != "http.curloptResolve=git.example:8080:203.0.113.5" {
		t.Fatalf("http with port: %q", got)
	}
	// An address literal is its own resolution; there is nothing to pin.
	u, _ = url.Parse("https://203.0.113.5/x.git")
	if got := pinArgs(u, []net.IP{net.ParseIP("203.0.113.5")}); !slices.Equal(got, []string{"-c", "http.followRedirects=false"}) {
		t.Fatalf("literal: %q", got)
	}
}
