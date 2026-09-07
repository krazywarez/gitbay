package e2e

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Atom feeds for releases, commits and an owner's public activity, read
// with no session; private repositories stay out of them (#192).
func TestAtomFeeds(t *testing.T) {
	inst := startInstance(t)
	aliceKey := inst.newKey(t, "alice")
	inst.admin(t, "admin", "user", "create", "alice", "--key", aliceKey+".pub")
	for _, args := range [][]string{
		{"repo", "create", "alice/app"},
		{"repo", "create", "alice/secret", "--private"},
	} {
		if _, errOut, code := inst.ssh(t, aliceKey, "", args...); code != 0 {
			t.Fatalf("%v: %s", args, errOut)
		}
	}
	if _, errOut, code := inst.ssh(t, aliceKey, "", "repo", "create", "alice/feed.atom"); code == 0 || !strings.Contains(errOut, ".atom") {
		t.Fatalf("a repository named like a feed was accepted: %d %s", code, errOut)
	}
	env := inst.gitEnv(aliceKey)
	for _, name := range []string{"app", "secret"} {
		dir := filepath.Join(t.TempDir(), name)
		mustGit(t, t.TempDir(), env, "clone", "-q", inst.sshURL("alice/"+name), dir)
		if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		mustGit(t, dir, env, "checkout", "-q", "-b", "main")
		mustGit(t, dir, env, "add", ".")
		mustGit(t, dir, env, "commit", "-q", "-m", "first commit on "+name)
		mustGit(t, dir, env, "tag", "v1.0")
		mustGit(t, dir, env, "push", "-q", "origin", "main", "v1.0")
		if _, errOut, code := inst.ssh(t, aliceKey, "", "release", "create", "alice/"+name, "v1.0",
			"--title", "'First light'", "--notes", "'notes for "+name+"'"); code != 0 {
			t.Fatalf("release create: %s", errOut)
		}
	}

	anon := &http.Client{}
	get := func(path string, want int) string {
		t.Helper()
		status, body := browserGet(t, anon, inst.base()+path)
		if status != want {
			t.Fatalf("GET %s: %d, want %d\n%s", path, status, want, body)
		}
		return body
	}
	resp, err := http.Get(inst.base() + "/alice/app/releases.atom")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/atom+xml") {
		t.Fatalf("content type %q", ct)
	}
	body := get("/alice/app/releases.atom", 200)
	for _, want := range []string{`xmlns="http://www.w3.org/2005/Atom"`, "<title>v1.0: First light</title>", "notes for app", "/alice/app/releases#v1.0"} {
		if !strings.Contains(body, want) {
			t.Errorf("releases feed missing %s:\n%s", want, body)
		}
	}
	for _, path := range []string{"/alice/app/log.atom", "/alice/app/log.atom/main"} {
		if body := get(path, 200); !strings.Contains(body, "<title>first commit on app</title>") || !strings.Contains(body, "<name>t</name>") {
			t.Errorf("%s:\n%s", path, body)
		}
	}
	get("/alice/app/log.atom/nope", 404)
	body = get("/alice/activity.atom", 200)
	if !strings.Contains(body, "release.created alice/app v1.0") || strings.Contains(body, "secret") {
		t.Errorf("activity feed:\n%s", body)
	}
	get("/nobody/activity.atom", 404)
	get("/alice/secret/releases.atom", 404)
	get("/alice/secret/log.atom", 404)

	// The pages say where their feed is.
	for path, feed := range map[string]string{
		"/alice/app/releases": "/alice/app/releases.atom",
		"/alice/app/log":      "/alice/app/log.atom/main",
		"/alice":              "/alice/activity.atom",
	} {
		if body := get(path, 200); !strings.Contains(body, `type="application/atom+xml" href="`+feed+`"`) {
			t.Errorf("%s has no discovery link to %s", path, feed)
		}
	}
}
