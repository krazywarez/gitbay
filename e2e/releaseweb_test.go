package e2e

import (
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestReleaseAndBuildWeb creates and edits a release and triggers a build
// from the browser, each through the command the CLI runs.
func TestReleaseAndBuildWeb(t *testing.T) {
	inst := startInstanceWith(t, "[web]\nmode = \"accounts\"\n")
	inst.runner = buildRunner(t)
	aliceKey := inst.newKey(t, "alice")
	inst.admin(t, "admin", "user", "create", "alice",
		"--key", aliceKey+".pub", "--email", "alice@example.test", "--verified")
	runnerKey := inst.newKey(t, "ci")
	inst.admin(t, "admin", "user", "create", "ci", "--key", runnerKey+".pub", "--admin")

	if _, errOut, code := inst.ssh(t, aliceKey, "", "repo", "create", "alice/app"); code != 0 {
		t.Fatalf("repo create: %s", errOut)
	}
	env := inst.gitEnv(aliceKey)
	work := t.TempDir()
	mustGit(t, work, env, "clone", inst.sshURL("alice/app"), "w")
	dir := filepath.Join(work, "w")
	os.MkdirAll(filepath.Join(dir, ".gitbay"), 0o755)
	os.WriteFile(filepath.Join(dir, ".gitbay", "ci.yml"),
		[]byte("jobs:\n  smoke:\n    steps:\n      - echo ok\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a\n"), 0o644)
	mustGit(t, dir, env, "checkout", "-q", "-b", "main")
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, env, "commit", "-q", "-m", "base")
	mustGit(t, dir, env, "tag", "-a", "v1.0", "-m", "first")
	mustGit(t, dir, env, "push", "-q", "origin", "main", "v1.0")

	alice := inst.login(t, aliceKey)
	base := inst.base() + "/alice/app"

	// The pushed tag is offered, and creating from it works.
	_, page := browserGet(t, alice, base+"/releases")
	if !strings.Contains(page, `value="v1.0"`) {
		t.Fatalf("tag not offered:\n%s", page)
	}
	if status, _ := browserPost(t, alice, base+"/releases", url.Values{
		"tag": {"v1.0"}, "title": {"First light"}, "notes": {"the **first** one"}}); status != 200 {
		t.Fatal("release create failed")
	}
	out, _, _ := inst.ssh(t, aliceKey, "", "release", "show", "alice/app", "v1.0", "--json")
	if !strings.Contains(out, "First light") || !strings.Contains(out, "the **first** one") {
		t.Fatalf("release not created:\n%s", out)
	}

	// Editing keeps the tag and replaces the fields.
	if status, _ := browserPost(t, alice, base+"/releases", url.Values{
		"action": {"edit"}, "tag": {"v1.0"}, "title": {"Second light"}, "notes": {"revised"}}); status != 200 {
		t.Fatal("release edit failed")
	}
	out, _, _ = inst.ssh(t, aliceKey, "", "release", "show", "alice/app", "v1.0", "--json")
	if !strings.Contains(out, "Second light") || !strings.Contains(out, "revised") {
		t.Fatalf("release not edited:\n%s", out)
	}
	// A released tag is no longer offered for creation.
	if _, page = browserGet(t, alice, base+"/releases"); strings.Contains(page, `<option value="v1.0"`) {
		t.Fatalf("released tag still offered:\n%s", page)
	}

	// Builds: the job from the config is offered and runs on demand.
	_, bpage := browserGet(t, alice, base+"/builds")
	if !strings.Contains(bpage, `value="smoke"`) {
		t.Fatalf("job not offered:\n%s", bpage)
	}
	if status, _ := browserPost(t, alice, base+"/builds", url.Values{"job": {"smoke"}}); status != 200 {
		t.Fatal("trigger failed")
	}
	inst.runnerOnce(t, runnerKey)
	out, _, _ = inst.ssh(t, aliceKey, "", "build", "list", "alice/app")
	if !strings.Contains(out, "smoke\tsuccess") {
		t.Fatalf("triggered build did not run:\n%s", out)
	}

	// A reader sees neither control.
	bobKey := inst.newKey(t, "bob")
	inst.admin(t, "admin", "user", "create", "bob", "--key", bobKey+".pub")
	bob := inst.login(t, bobKey)
	if _, p := browserGet(t, bob, base+"/builds"); strings.Contains(p, "Run a job now") {
		t.Fatal("reader sees the trigger control")
	}
	if _, p := browserGet(t, bob, base+"/releases"); strings.Contains(p, "New release") {
		t.Fatal("reader sees the release form")
	}
}
