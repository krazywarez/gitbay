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
	if status != 200 || !strings.Contains(body, `href="/explore"`) || !strings.Contains(body, "A git forge you drive from the terminal") {
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
		`aria-label="Pinned repositories"`, `class="pins"`, `</span>app</a>`, // the dashboard lists the pinned repo
		"Waiting on your review", "assigned to you", "Recent activity",
		"from bob", "alice/app!1", "todo one", "alice/app#1",
		`href="/alice/app/mrs/1"`, `href="/alice/app/issues/1"`,
		"Yours anywhere, and every one in a repository you can write to.",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("dashboard missing %q", want)
		}
	}

	// The empty queue is a tile with a zero; a populated one is a tile
	// that links to its rows, which render in the middle column.
	if !strings.Contains(body, `<div class="tile"><b>0</b><span>assigned to you</span></div>`) {
		t.Fatalf("dashboard missing the zero tile for assigned:\n%s", body)
	}
	if !strings.Contains(body, `<a class="tile wants" href="#reviews"><b>1</b><span>waiting on your review</span></a>`) {
		t.Fatalf("dashboard missing the review tile:\n%s", body)
	}
	if !strings.Contains(body, `<h2 id="reviews">Waiting on your review`) || strings.Contains(body, `<h2 class="empty">`) {
		t.Fatalf("queues do not render as tiles plus rows:\n%s", body)
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

// The dashboard control command returns the same aggregate as the web
// dashboard — review queue, assigned/open work, pins, and activity — in
// one read. Builds remain for clients that already consume them.
func TestDashboardCommand(t *testing.T) {
	inst := startInstance(t)
	aliceKey := inst.newKey(t, "alice")
	bobKey := inst.newKey(t, "bob")
	inst.admin(t, "admin", "user", "create", "alice", "--key", aliceKey+".pub")
	inst.admin(t, "admin", "user", "create", "bob", "--key", bobKey+".pub")

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
	os.MkdirAll(filepath.Join(dir, ".gitbay"), 0o755)
	os.WriteFile(filepath.Join(dir, ".gitbay", "ci.yml"), []byte(
		"jobs:\n  test:\n    steps:\n      - echo ok\n"), 0o644)
	mustGit(t, dir, env, "checkout", "-q", "-b", "main")
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, env, "commit", "-q", "-m", "base")
	mustGit(t, dir, env, "push", "-q", "origin", "main")
	mustGit(t, dir, env, "checkout", "-q", "-b", "feat")
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a\n"), 0o644)
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
	if _, _, code := inst.ssh(t, aliceKey, "", "issue", "assign", "alice/app", "1", "--add", "alice"); code != 0 {
		t.Fatal("assign failed")
	}
	if _, _, code := inst.ssh(t, aliceKey, "", "repo", "pin", "alice/app"); code != 0 {
		t.Fatal("pin failed")
	}
	if _, _, code := inst.ssh(t, bobKey, "", "repo", "pin", "alice/app"); code != 0 {
		t.Fatal("bob pin failed")
	}

	out, errOut, code := inst.ssh(t, aliceKey, "", "dashboard", "--json")
	if code != 0 {
		t.Fatalf("dashboard: %s", errOut)
	}
	var env2 struct {
		Data struct {
			Reviews []struct {
				Repo   string `json:"repo"`
				Number int64  `json:"number"`
				Title  string `json:"title"`
			} `json:"review_queue"`
			Pinned []struct {
				Path string `json:"path"`
			} `json:"pinned"`
			MRs []struct {
				Repo   string `json:"repo"`
				Number int64  `json:"number"`
				Title  string `json:"title"`
				Author string `json:"author"`
				State  string `json:"state"`
			} `json:"open_mrs"`
			Assigned []struct {
				Repo   string `json:"repo"`
				Number int64  `json:"number"`
				Title  string `json:"title"`
			} `json:"assigned_issues"`
			Issues []struct {
				Repo   string `json:"repo"`
				Number int64  `json:"number"`
				Title  string `json:"title"`
			} `json:"open_issues"`
			Activity []struct {
				Repo  string `json:"repo"`
				Actor string `json:"actor"`
				Kind  string `json:"kind"`
			} `json:"recent_activity"`
			Builds []struct {
				Repo   string `json:"repo"`
				Job    string `json:"job"`
				Status string `json:"status"`
				Ref    string `json:"ref"`
			} `json:"builds"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &env2); err != nil {
		t.Fatalf("bad json: %v\n%s", err, out)
	}
	d := env2.Data
	if len(d.Pinned) != 1 || d.Pinned[0].Path != "alice/app" {
		t.Fatalf("pinned = %+v", d.Pinned)
	}
	if len(d.MRs) != 1 || d.MRs[0].Repo != "alice/app" || d.MRs[0].Number != 1 ||
		d.MRs[0].Title != "from bob" || d.MRs[0].Author != "bob" || d.MRs[0].State != "open" {
		t.Fatalf("open_mrs = %+v", d.MRs)
	}
	if len(d.Reviews) != 1 || d.Reviews[0].Repo != "alice/app" || d.Reviews[0].Number != 1 ||
		d.Reviews[0].Title != "from bob" {
		t.Fatalf("review_queue = %+v", d.Reviews)
	}
	if len(d.Assigned) != 1 || d.Assigned[0].Repo != "alice/app" || d.Assigned[0].Number != 1 ||
		d.Assigned[0].Title != "todo one" {
		t.Fatalf("assigned_issues = %+v", d.Assigned)
	}
	if len(d.Issues) != 1 || d.Issues[0].Repo != "alice/app" || d.Issues[0].Number != 1 ||
		d.Issues[0].Title != "todo one" {
		t.Fatalf("open_issues = %+v", d.Issues)
	}
	if len(d.Activity) == 0 || d.Activity[0].Repo != "alice/app" {
		t.Fatalf("recent_activity = %+v", d.Activity)
	}
	// Both pushes hit main and feat; each queues the ci.yml job.
	if len(d.Builds) == 0 || d.Builds[0].Repo != "alice/app" || d.Builds[0].Job != "test" ||
		d.Builds[0].Status != "pending" {
		t.Fatalf("builds = %+v", d.Builds)
	}

	// Bob is not assigned and pinned repos he can no longer read disappear.
	if _, _, code := inst.ssh(t, aliceKey, "", "repo", "settings", "visibility", "alice/app", "private"); code != 0 {
		t.Fatal("visibility failed")
	}
	if _, _, code := inst.ssh(t, aliceKey, "", "repo", "access", "revoke", "alice/app", "bob"); code != 0 {
		t.Fatal("revoke failed")
	}
	out, _, code = inst.ssh(t, bobKey, "", "dashboard", "--json")
	if code != 0 {
		t.Fatal("bob dashboard failed")
	}
	if err := json.Unmarshal([]byte(out), &env2); err != nil {
		t.Fatalf("bad json: %v\n%s", err, out)
	}
	if len(env2.Data.Pinned) != 0 {
		t.Fatalf("bob still sees pinned = %+v", env2.Data.Pinned)
	}
	if len(env2.Data.Assigned) != 0 {
		t.Fatalf("bob assigned = %+v", env2.Data.Assigned)
	}
	if len(env2.Data.Builds) != 0 {
		t.Fatalf("bob builds = %+v", env2.Data.Builds)
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
	if !strings.Contains(after, `<div class="tile"><b>0</b><span>waiting on your review</span></div>`) {
		t.Fatalf("reviewed MR still waiting:\n%s", after)
	}
}

// D04/D05: two jobs on one commit fold into a single feed line, marked
// with the worse of the two outcomes, and shown with a relative time
// carrying the exact UTC time in its title.
func TestDashboardFeedFoldsBuildRun(t *testing.T) {
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
	work := t.TempDir()
	env := inst.gitEnv(aliceKey)
	mustGit(t, work, env, "clone", inst.sshURL("alice/app"), "w")
	dir := filepath.Join(work, "w")
	os.MkdirAll(filepath.Join(dir, ".gitbay"), 0o755)
	os.WriteFile(filepath.Join(dir, ".gitbay", "ci.yml"), []byte(
		"jobs:\n  ok:\n    steps:\n      - echo fine\n  broken:\n    steps:\n      - \"false\"\n"), 0o644)
	mustGit(t, dir, env, "checkout", "-q", "-b", "main")
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, env, "commit", "-q", "-m", "base")
	mustGit(t, dir, env, "push", "-q", "origin", "main")
	sha := strings.TrimSpace(mustGit(t, dir, env, "rev-parse", "HEAD"))

	// The runner processes both jobs ("broken" sorts first).
	inst.runnerOnce(t, runnerKey)
	inst.runnerOnce(t, runnerKey)

	_, body := browserGet(t, inst.login(t, aliceKey), inst.base()+"/")
	if !strings.Contains(body, "ran 2 jobs on") {
		t.Fatalf("feed did not fold the two jobs into one run:\n%s", body)
	}
	if !strings.Contains(body, `class="dot bad"`) {
		t.Fatalf("feed did not mark the run with the worse (failure) status:\n%s", body)
	}
	if !strings.Contains(body, sha[:10]) {
		t.Fatalf("feed missing the short sha %q:\n%s", sha[:10], body)
	}
	// A build reported moments ago renders as "just now"; ago() only
	// switches to "N ago" past a minute, so either form proves the
	// relative-time rendering rather than the raw timestamp.
	if !strings.Contains(body, ">just now<") && !strings.Contains(body, " ago<") {
		t.Fatalf("feed missing a relative time:\n%s", body)
	}
	if !strings.Contains(body, "title=\"") || !strings.Contains(body, " UTC\"") {
		t.Fatalf("feed missing the exact time in a title:\n%s", body)
	}
}
