package e2e

import (
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Forking from the browser completes the contributor path: fork, edit a
// file in the fork, open the merge request against the parent, without a
// terminal at any step (#174).
func TestForkWeb(t *testing.T) {
	inst := startInstanceWith(t, "[web]\nmode = \"accounts\"\n")
	aliceKey := inst.newKey(t, "alice")
	bobKey := inst.newKey(t, "bob")
	inst.admin(t, "admin", "user", "create", "alice", "--key", aliceKey+".pub",
		"--email", "alice@example.test", "--verified")
	inst.admin(t, "admin", "user", "create", "bob", "--key", bobKey+".pub",
		"--email", "bob@example.test", "--verified")

	if _, errOut, code := inst.ssh(t, aliceKey, "", "repo", "create", "alice/app"); code != 0 {
		t.Fatalf("repo create: %s", errOut)
	}
	env := inst.gitEnv(aliceKey)
	work := t.TempDir()
	mustGit(t, work, env, "clone", inst.sshURL("alice/app"), "w")
	dir := filepath.Join(work, "w")
	os.WriteFile(filepath.Join(dir, "f.txt"), []byte("x\n"), 0o644)
	mustGit(t, dir, env, "checkout", "-q", "-b", "main")
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, env, "commit", "-q", "-m", "base")
	mustGit(t, dir, env, "push", "-q", "origin", "main")

	bob := inst.login(t, bobKey)
	base := inst.base() + "/alice/app"

	// The control is on the repository header for a signed-in visitor.
	if _, page := browserGet(t, bob, base); !strings.Contains(page, `/alice/app/fork"`) {
		t.Fatalf("no fork control on the repository header:\n%s", page)
	}

	// Forking lands on the fork, which carries the parent's history.
	if status, body := browserPost(t, bob, base+"/fork", url.Values{}); status != 200 ||
		!strings.Contains(body, "bob/app") {
		t.Fatalf("fork did not land on the new repository: %d\n%s", status, body)
	}
	out, _, _ := inst.ssh(t, bobKey, "", "repo", "list", "--json")
	if !strings.Contains(out, `"path":"bob/app"`) {
		t.Fatalf("fork not created:\n%s", out)
	}

	// Editing a file in the fork from the browser — the editor commits to
	// the ref in the URL — then proposing it to the parent through the
	// source picker.
	if status, _ := browserPost(t, bob, inst.base()+"/bob/app/edit/main/f.txt", url.Values{
		"content": {"y\n"}, "message": {"change f"}}); status != 200 {
		t.Fatal("web edit in the fork failed")
	}
	if _, page := browserGet(t, bob, base+"/mrs/new"); !strings.Contains(page, `value="bob/app:main"`) {
		t.Fatalf("the fork's branch is not offered:\n%s", page)
	}
	if status, _ := browserPost(t, bob, base+"/mrs/new", url.Values{
		"source": {"bob/app:main"}, "target": {"main"}, "title": {"change"}}); status != 200 {
		t.Fatal("mr create from the fork failed")
	}
	if out, _, _ := inst.ssh(t, aliceKey, "", "mr", "show", "alice/app", "1", "--json"); !strings.Contains(out, `"source":"bob/app:main"`) {
		t.Fatalf("merge request not opened from the fork:\n%s", out)
	}

	// Forking twice collides on the name, and says so on the page rather
	// than failing silently.
	_, body := browserPost(t, bob, base+"/fork", url.Values{})
	if !strings.Contains(body, `class="error"`) {
		t.Errorf("a second fork reported nothing:\n%s", body)
	}
}
