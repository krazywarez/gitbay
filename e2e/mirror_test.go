package e2e

import (
	"encoding/json"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// gitHTTPRemote serves a bare repository over smart HTTP (push enabled),
// standing in for GitHub in mirror tests.
func gitHTTPRemote(t *testing.T) (url, bareDir string) {
	t.Helper()
	parent := t.TempDir()
	bareDir = filepath.Join(parent, "remote.git")
	for _, args := range [][]string{
		{"init", "--bare", "--initial-branch=main", bareDir},
		{"-C", bareDir, "config", "http.receivepack", "true"},
	} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	execPath, err := exec.Command("git", "--exec-path").Output()
	if err != nil {
		t.Fatal(err)
	}
	h := &cgi.Handler{
		Path: filepath.Join(strings.TrimSpace(string(execPath)), "git-http-backend"),
		Env:  []string{"GIT_PROJECT_ROOT=" + parent, "GIT_HTTP_EXPORT_ALL=1"},
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv.URL + "/remote.git", bareDir
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(150 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestMirrors(t *testing.T) {
	t.Setenv("GITBAY_MIRROR_TICK", "200ms")
	inst := startInstanceWith(t, "[webhooks]\nallow_local = true\n[web]\nmode = \"accounts\"\n")
	aliceKey := inst.newKey(t, "alice")
	bobKey := inst.newKey(t, "bob")
	inst.admin(t, "admin", "user", "create", "alice", "--key", aliceKey+".pub")
	inst.admin(t, "admin", "user", "create", "bob", "--key", bobKey+".pub")

	// ---- push mirror: local pushes propagate to the remote.
	remoteURL, remoteBare := gitHTTPRemote(t)
	if _, errOut, code := inst.ssh(t, aliceKey, "", "repo", "create", "alice/app"); code != 0 {
		t.Fatalf("repo create: %s", errOut)
	}
	if _, _, code := inst.ssh(t, bobKey, "", "repo", "mirror", "add", "alice/app", remoteURL, "--direction", "push"); code != 4 {
		t.Fatal("non-admin added a mirror")
	}
	if _, errOut, code := inst.ssh(t, aliceKey, "", "repo", "mirror", "add", "alice/app", remoteURL, "--direction", "push"); code != 0 {
		t.Fatalf("mirror add: %s", errOut)
	}

	work := t.TempDir()
	env := inst.gitEnv(aliceKey)
	mustGit(t, work, env, "clone", inst.sshURL("alice/app"), "w")
	dir := filepath.Join(work, "w")
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a\n"), 0o644)
	mustGit(t, dir, env, "checkout", "-q", "-b", "main")
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, env, "commit", "-q", "-m", "base")
	mustGit(t, dir, env, "push", "-q", "origin", "main")
	head := strings.TrimSpace(mustGit(t, dir, env, "rev-parse", "HEAD"))

	waitFor(t, "push mirror sync", func() bool {
		out, _ := exec.Command("git", "-C", remoteBare, "rev-parse", "refs/heads/main").Output()
		return strings.TrimSpace(string(out)) == head
	})
	out, _, _ := inst.ssh(t, aliceKey, "", "repo", "mirror", "list", "alice/app", "--json")
	if !strings.Contains(out, `"last_sync":"`) || strings.Contains(out, "token") ||
		strings.Contains(out, `"last_error":"`) {
		t.Fatalf("mirror list after sync: %s", out)
	}

	// ---- status surfacing: repo show carries mirrors for admins only.
	out, _, _ = inst.ssh(t, aliceKey, "", "repo", "show", "alice/app", "--json")
	if !strings.Contains(out, `"mirrors":[`) || !strings.Contains(out, `"last_sync":"`) ||
		strings.Contains(out, "token") {
		t.Fatalf("repo show missing mirror status: %s", out)
	}
	out, _, _ = inst.ssh(t, bobKey, "", "repo", "show", "alice/app", "--json")
	if strings.Contains(out, `"mirrors"`) {
		t.Fatalf("repo show leaked mirrors to non-admin: %s", out)
	}

	// The web repo page shows the mirror line to the admin, not to visitors.
	out, errOut, code := inst.ssh(t, aliceKey, "", "web", "login", "--json")
	if code != 0 {
		t.Fatalf("web login: %s", errOut)
	}
	var loginEnv struct {
		Data struct {
			URL string `json:"url"`
		} `json:"data"`
	}
	json.Unmarshal([]byte(out), &loginEnv)
	loginPath := loginEnv.Data.URL[strings.Index(loginEnv.Data.URL, "/login"):]
	browser := newBrowser(t)
	if status, _ := browserGet(t, browser, inst.base()+loginPath); status != 200 {
		t.Fatalf("login: %d", status)
	}
	if _, body := browserGet(t, browser, inst.base()+"/alice/app"); !strings.Contains(body, "mirrors to") {
		t.Fatalf("admin repo page missing mirror line:\n%s", body)
	}
	if _, body := browserGet(t, newBrowser(t), inst.base()+"/alice/app"); strings.Contains(body, "mirrors to") {
		t.Fatalf("anonymous repo page shows mirror line:\n%s", body)
	}

	// ---- pull mirror: local repo follows the remote and refuses pushes.
	srcURL, srcBare := gitHTTPRemote(t)
	seed := t.TempDir()
	mustGit(t, seed, env, "clone", "-q", srcBare, "s")
	sdir := filepath.Join(seed, "s")
	os.WriteFile(filepath.Join(sdir, "up.txt"), []byte("upstream\n"), 0o644)
	mustGit(t, sdir, env, "checkout", "-q", "-b", "main")
	mustGit(t, sdir, env, "add", ".")
	mustGit(t, sdir, env, "commit", "-q", "-m", "upstream commit")
	mustGit(t, sdir, env, "push", "-q", "origin", "main")
	upstreamHead := strings.TrimSpace(mustGit(t, sdir, env, "rev-parse", "HEAD"))

	if _, _, code := inst.ssh(t, aliceKey, "", "repo", "create", "alice/follow"); code != 0 {
		t.Fatal("repo create failed")
	}
	if _, errOut, code := inst.ssh(t, aliceKey, "", "repo", "mirror", "add", "alice/follow", srcURL, "--direction", "pull"); code != 0 {
		t.Fatalf("pull mirror add: %s", errOut)
	}
	waitFor(t, "pull mirror sync", func() bool {
		out, _, _ := inst.ssh(t, aliceKey, "", "repo", "log", "alice/follow", "--json")
		return strings.Contains(out, upstreamHead)
	})
	// Local pushes are refused while the pull mirror exists.
	work2 := t.TempDir()
	mustGit(t, work2, env, "clone", "-q", inst.sshURL("alice/follow"), "f")
	fdir := filepath.Join(work2, "f")
	os.WriteFile(filepath.Join(fdir, "no.txt"), []byte("n\n"), 0o644)
	mustGit(t, fdir, env, "add", ".")
	mustGit(t, fdir, env, "commit", "-q", "-m", "local change")
	if out, code := gitRun(t, fdir, env, "push", "origin", "HEAD:main"); code == 0 || !strings.Contains(out, "pull mirror") {
		t.Fatalf("push to pull mirror: exit %d\n%s", code, out)
	}
	// Removing the mirror restores pushes.
	out, _, _ = inst.ssh(t, aliceKey, "", "repo", "mirror", "list", "alice/follow", "--json")
	id := out[strings.Index(out, `"id":`)+5:]
	id = id[:strings.IndexAny(id, ",}")]
	if _, _, code := inst.ssh(t, aliceKey, "", "repo", "mirror", "remove", "alice/follow", strings.TrimSpace(id)); code != 0 {
		t.Fatal("mirror remove failed")
	}
	mustGit(t, fdir, env, "push", "-q", "origin", "HEAD:main")

	// ---- failure visibility: a dead remote records an error.
	deadURL := remoteURL + "-gone"
	if _, _, code := inst.ssh(t, aliceKey, "", "repo", "mirror", "add", "alice/follow", deadURL, "--direction", "push"); code != 0 {
		t.Fatal("dead mirror add failed")
	}
	if _, _, code := inst.ssh(t, aliceKey, "", "repo", "mirror", "sync", "alice/follow"); code != 0 {
		t.Fatal("mirror sync failed")
	}
	waitFor(t, "failure recorded", func() bool {
		out, _, _ := inst.ssh(t, aliceKey, "", "repo", "mirror", "list", "alice/follow", "--json")
		return strings.Contains(out, `"last_error":"git push`)
	})
	// The failure is visible on the admin's web repo page.
	if _, body := browserGet(t, browser, inst.base()+"/alice/follow"); !strings.Contains(body, "sync error:") {
		t.Fatalf("admin repo page missing sync error:\n%s", body)
	}
}

func TestMirrorSSRFGuard(t *testing.T) {
	inst := startInstance(t) // default posture: allow_local off
	aliceKey := inst.newKey(t, "alice")
	inst.admin(t, "admin", "user", "create", "alice", "--key", aliceKey+".pub")
	if _, _, code := inst.ssh(t, aliceKey, "", "repo", "create", "alice/app"); code != 0 {
		t.Fatal("repo create failed")
	}
	_, errOut, code := inst.ssh(t, aliceKey, "", "repo", "mirror", "add", "alice/app",
		"http://127.0.0.1:9999/x.git", "--direction", "push")
	if code != 2 || !strings.Contains(errOut, "SSRF") {
		t.Fatalf("local mirror allowed: exit %d, %s", code, errOut)
	}
}
