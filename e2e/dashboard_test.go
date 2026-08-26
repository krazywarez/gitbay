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
	if status != 200 || !strings.Contains(body, ">Dashboard</h1>") {
		t.Fatalf("dashboard: %d", status)
	}
	for _, want := range []string{
		">Pinned</h2>", ">app<", // pinned group in the aside
		"Waiting on your review", "Assigned to you", "Recent activity",
		"from bob", "alice/app!1", "todo one", "alice/app#1",
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
	if strings.Contains(body, `class="difftable"`) {
		t.Fatal("diff rendered on the conversation view")
	}
	_, body = inst.get(t, "/alice/app/mrs/1?view=diff")
	if !strings.Contains(body, "1 file changed") || !strings.Contains(body, `class="difftable"`) {
		t.Fatalf("diff view missing stat or patch:\n%s", body)
	}
}

// TestDashboardQueues covers the parts of the dashboard that answer "what
// needs me": the review queue, assigned issues, and the activity feed.
func TestDashboardQueues(t *testing.T) {
	inst := startInstanceWith(t, "[web]\nmode = \"accounts\"\n")
	aliceKey := inst.newKey(t, "alice")
	bobKey := inst.newKey(t, "bob")
	inst.admin(t, "admin", "user", "create", "alice",
		"--key", aliceKey+".pub", "--email", "alice@example.test", "--verified")
	inst.admin(t, "admin", "user", "create", "bob",
		"--key", bobKey+".pub", "--email", "bob@example.test", "--verified")

	if _, errOut, code := inst.ssh(t, aliceKey, "", "repo", "create", "alice/app"); code != 0 {
		t.Fatalf("repo create: %s", errOut)
	}
	if _, errOut, code := inst.ssh(t, aliceKey, "", "repo", "access", "grant", "alice/app", "bob", "write"); code != 0 {
		t.Fatalf("grant: %s", errOut)
	}
	env := inst.gitEnv(aliceKey)
	work := t.TempDir()
	mustGit(t, work, env, "clone", inst.sshURL("alice/app"), "w")
	dir := filepath.Join(work, "w")
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a\n"), 0o644)
	mustGit(t, dir, env, "checkout", "-q", "-b", "main")
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, env, "commit", "-q", "-m", "base")
	mustGit(t, dir, env, "push", "-q", "origin", "main")

	// Bob opens a merge request: it lands in alice's review queue.
	bobEnv := inst.gitEnv(bobKey)
	bobWork := t.TempDir()
	mustGit(t, bobWork, bobEnv, "clone", inst.sshURL("alice/app"), "w")
	bobDir := filepath.Join(bobWork, "w")
	mustGit(t, bobDir, bobEnv, "checkout", "-q", "-b", "fix", "origin/main")
	os.WriteFile(filepath.Join(bobDir, "b.txt"), []byte("b\n"), 0o644)
	mustGit(t, bobDir, bobEnv, "add", ".")
	mustGit(t, bobDir, bobEnv, "commit", "-q", "-m", "the fix")
	mustGit(t, bobDir, bobEnv, "push", "-q", "origin", "fix")
	if _, errOut, code := inst.ssh(t, bobKey, "", "mr", "create", "alice/app",
		"--source", "fix", "--target", "main", "--title", "'needs a look'"); code != 0 {
		t.Fatalf("mr create: %s", errOut)
	}
	// And an issue assigned to alice.
	if _, errOut, code := inst.ssh(t, bobKey, "", "issue", "create", "alice/app", "--title", "'please handle'"); code != 0 {
		t.Fatalf("issue create: %s", errOut)
	}
	if _, errOut, code := inst.ssh(t, aliceKey, "", "issue", "assign", "alice/app", "1", "--add", "alice"); code != 0 {
		t.Fatalf("assign: %s", errOut)
	}

	_, body := browserGet(t, inst.login(t, aliceKey), inst.base()+"/")
	if !strings.Contains(body, "needs a look") {
		t.Fatalf("review queue missing the MR:\n%s", body)
	}
	if !strings.Contains(body, "please handle") {
		t.Fatalf("assigned issues missing the issue:\n%s", body)
	}
	// The feed reports what happened, phrased and linked.
	for _, want := range []string{"opened merge request", "opened issue", `href="/alice/app/mrs/1"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("feed missing %q:\n%s", want, body)
		}
	}

	// Once alice reviews, the merge request leaves her queue.
	if _, errOut, code := inst.ssh(t, aliceKey, "", "mr", "review", "alice/app", "1", "--approve"); code != 0 {
		t.Fatalf("review: %s", errOut)
	}
	_, after := browserGet(t, inst.login(t, aliceKey), inst.base()+"/")
	queue := after[strings.Index(after, "Waiting on your review"):strings.Index(after, "Assigned to you")]
	if strings.Contains(queue, "needs a look") {
		t.Fatalf("reviewed MR still waiting:\n%s", queue)
	}
}
