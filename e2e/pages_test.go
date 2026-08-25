package e2e

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// pagesGet fetches a path with a pages Host header against the instance.
func (i *instance) pagesGet(t *testing.T, host, path string) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest("GET", fmt.Sprintf("http://127.0.0.1:%d%s", i.httpPort, path), nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = host
	resp, err := (&http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp, string(body)
}

func TestPages(t *testing.T) {
	inst := startInstanceWith(t, "[pages]\ndomain = \"p.test\"\n")
	aliceKey := inst.newKey(t, "alice")
	inst.admin(t, "admin", "user", "create", "alice", "--key", aliceKey+".pub")

	env := inst.gitEnv(aliceKey)
	pushPages := func(repo string, files map[string]string) {
		t.Helper()
		if _, errOut, code := inst.ssh(t, aliceKey, "", "repo", "create", repo); code != 0 {
			t.Fatalf("create %s: %s", repo, errOut)
		}
		work := t.TempDir()
		mustGit(t, work, env, "clone", inst.sshURL(repo), "w")
		dir := filepath.Join(work, "w")
		for name, content := range files {
			os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o755)
			os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644)
		}
		mustGit(t, dir, env, "checkout", "-q", "-b", "pages")
		mustGit(t, dir, env, "add", ".")
		mustGit(t, dir, env, "commit", "-q", "-m", "site")
		mustGit(t, dir, env, "push", "-q", "origin", "pages")
	}

	pushPages("alice/pages", map[string]string{
		"index.html": "<h1>alice root</h1><script>x=1</script>",
	})
	pushPages("alice/site", map[string]string{
		"index.html":       "<h1>project site</h1>",
		"style.css":        "body{color:red}",
		"guide/index.html": "<h1>guide</h1>",
	})

	// Root site from the "pages" repo, scripts intact, no forge CSP.
	resp, body := inst.pagesGet(t, "alice.p.test", "/")
	if resp.StatusCode != 200 || !strings.Contains(body, "alice root") || !strings.Contains(body, "<script>") {
		t.Fatalf("root site: %d\n%s", resp.StatusCode, body)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("root content-type: %s", ct)
	}
	if resp.Header.Get("Content-Security-Policy") != "" {
		t.Fatal("forge CSP leaked onto a pages response")
	}

	// Project site under /<repo>/, with a redirect adding the slash.
	if resp, _ = inst.pagesGet(t, "alice.p.test", "/site"); resp.StatusCode != 301 {
		t.Fatalf("bare project path: %d", resp.StatusCode)
	}
	if resp, body = inst.pagesGet(t, "alice.p.test", "/site/"); !strings.Contains(body, "project site") {
		t.Fatalf("project index: %d\n%s", resp.StatusCode, body)
	}
	if resp, _ = inst.pagesGet(t, "alice.p.test", "/site/style.css"); !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/css") {
		t.Fatalf("css content-type: %s", resp.Header.Get("Content-Type"))
	}
	// Directory paths inside a site serve their index and gain a slash.
	if resp, _ = inst.pagesGet(t, "alice.p.test", "/site/guide"); resp.StatusCode != 301 {
		t.Fatalf("dir redirect: %d", resp.StatusCode)
	}
	if _, body = inst.pagesGet(t, "alice.p.test", "/site/guide/"); !strings.Contains(body, "guide") {
		t.Fatalf("dir index:\n%s", body)
	}

	// Private repos never serve pages; unknown owners and the apex 404.
	if _, errOut, code := inst.ssh(t, aliceKey, "", "repo", "create", "alice/secret", "--private"); code != 0 {
		t.Fatalf("create secret: %s", errOut)
	}
	work := t.TempDir()
	mustGit(t, work, env, "clone", inst.sshURL("alice/secret"), "w")
	sdir := filepath.Join(work, "w")
	os.WriteFile(filepath.Join(sdir, "index.html"), []byte("hidden"), 0o644)
	mustGit(t, sdir, env, "checkout", "-q", "-b", "pages")
	mustGit(t, sdir, env, "add", ".")
	mustGit(t, sdir, env, "commit", "-q", "-m", "s")
	mustGit(t, sdir, env, "push", "-q", "origin", "pages")
	for _, tc := range []struct{ host, path string }{
		{"alice.p.test", "/secret/"},
		{"bob.p.test", "/"},
		{"p.test", "/"},
	} {
		if resp, _ = inst.pagesGet(t, tc.host, tc.path); resp.StatusCode != 404 {
			t.Fatalf("%s%s: %d, want 404", tc.host, tc.path, resp.StatusCode)
		}
	}

	// The forge itself still answers on its own host.
	if status, _ := inst.get(t, "/explore"); status != 200 {
		t.Fatalf("forge routes broken: %d", status)
	}
}
