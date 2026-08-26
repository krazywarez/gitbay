package e2e

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDashboard(t *testing.T) {
	inst := startInstanceWith(t, "[web]\nmode = \"accounts\"\n")
	aliceKey := inst.newKey(t, "alice")
	bobKey := inst.newKey(t, "bob")
	inst.admin(t, "admin", "user", "create", "alice",
		"--key", aliceKey+".pub", "--email", "alice@example.test", "--verified")
	inst.admin(t, "admin", "user", "create", "bob", "--key", bobKey+".pub")

	// Alice's repo with an open issue and an open MR authored by bob.
	if _, errOut, code := inst.ssh(t, aliceKey, "", "repo", "create", "alice/app"); code != 0 {
		t.Fatalf("repo create: %s", errOut)
	}
	if _, _, code := inst.ssh(t, aliceKey, "", "repo", "access", "grant", "alice/app", "bob", "write"); code != 0 {
		t.Fatal("grant failed")
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
	mustGit(t, dir, env, "checkout", "-q", "-b", "feat")
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a\nb\n"), 0o644)
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, env, "commit", "-q", "-m", "feat")
	mustGit(t, dir, env, "push", "-q", "origin", "feat")
	if _, _, code := inst.ssh(t, bobKey, "", "mr", "create", "alice/app",
		"--source", "feat", "--target", "main", "--title", "'from bob'"); code != 0 {
		t.Fatal("mr create failed")
	}
	if _, _, code := inst.ssh(t, aliceKey, "", "issue", "create", "alice/app", "--title", "'todo one'"); code != 0 {
		t.Fatal("issue create failed")
	}

	// Pins: read access required; unpinning something never pinned 404s.
	if _, _, code := inst.ssh(t, aliceKey, "", "repo", "pin", "alice/app"); code != 0 {
		t.Fatal("pin failed")
	}
	if _, _, code := inst.ssh(t, aliceKey, "", "repo", "unpin", "alice/app"); code != 0 {
		t.Fatal("unpin failed")
	}
	if _, _, code := inst.ssh(t, aliceKey, "", "repo", "unpin", "alice/app"); code != 3 {
		t.Fatal("double unpin should be not-found")
	}
	if _, _, code := inst.ssh(t, aliceKey, "", "repo", "pin", "alice/app"); code != 0 {
		t.Fatal("re-pin failed")
	}

	// Anonymous homepage: landing with explore link, repos on /explore.
	status, body := inst.get(t, "/")
	if status != 200 || !strings.Contains(body, `href="/explore"`) || !strings.Contains(body, "CLI-first") {
		t.Fatalf("landing: %d", status)
	}
	if strings.Contains(body, "alice/app") {
		t.Fatal("landing lists repositories")
	}
	_, body = inst.get(t, "/explore")
	if !strings.Contains(body, "alice/app") {
		t.Fatal("explore missing public repo")
	}

	// Logged-in homepage: dashboard with pinned repo and open items.
	out, errOut, code := inst.ssh(t, aliceKey, "", "web", "login", "--json")
	if code != 0 {
		t.Fatalf("web login: %s", errOut)
	}
	var env2 struct {
		Data struct {
			URL string `json:"url"`
		} `json:"data"`
	}
	json.Unmarshal([]byte(out), &env2)
	loginPath := env2.Data.URL[strings.Index(env2.Data.URL, "/login"):]
	browser := newBrowser(t)
	if status, _ := browserGet(t, browser, inst.base()+loginPath); status != 200 {
		t.Fatalf("login: %d", status)
	}
	status, body = browserGet(t, browser, inst.base()+"/")
	if status != 200 || !strings.Contains(body, `logged in as <a href="/alice">alice</a>`) {
		t.Fatalf("dashboard: %d", status)
	}
	for _, want := range []string{
		">Pinned</h2>", ">app<", // pinned card
		"alice/app!1 from bob", "alice/app#1 todo one",
		`href="/alice/app/mrs/1"`, `href="/alice/app/issues/1"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("dashboard missing %q", want)
		}
	}

	// The diff has its own view rather than a fold at the foot of the
	// conversation: the default view offers it, and asking for it renders
	// the stat line and the patch.
	_, body = inst.get(t, "/alice/app/mrs/1")
	if !strings.Contains(body, `href="/alice/app/mrs/1?view=diff"`) {
		t.Fatal("merge request missing the files-changed view")
	}
	if strings.Contains(body, `class="diff"`) {
		t.Fatal("diff rendered on the conversation view")
	}
	_, body = inst.get(t, "/alice/app/mrs/1?view=diff")
	if !strings.Contains(body, "1 file changed") || !strings.Contains(body, `class="diff"`) {
		t.Fatalf("diff view missing stat or patch:\n%s", body)
	}
}
