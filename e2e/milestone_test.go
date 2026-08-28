package e2e

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMilestonesAndTemplates(t *testing.T) {
	inst := startInstanceWith(t, "[web]\nmode = \"accounts\"\n")
	aliceKey := inst.newKey(t, "alice")
	bobKey := inst.newKey(t, "bob")
	inst.admin(t, "admin", "user", "create", "alice",
		"--key", aliceKey+".pub", "--email", "alice@example.test", "--verified")
	inst.admin(t, "admin", "user", "create", "bob", "--key", bobKey+".pub")

	if _, errOut, code := inst.ssh(t, aliceKey, "", "repo", "create", "alice/app"); code != 0 {
		t.Fatalf("repo create: %s", errOut)
	}
	work := t.TempDir()
	env := inst.gitEnv(aliceKey)
	mustGit(t, work, env, "clone", inst.sshURL("alice/app"), "w")
	dir := filepath.Join(work, "w")
	os.MkdirAll(filepath.Join(dir, ".gitbay"), 0o755)
	os.WriteFile(filepath.Join(dir, ".gitbay", "issue-template.md"),
		[]byte("## Steps to reproduce\n\n## Expected\n"), 0o644)
	os.WriteFile(filepath.Join(dir, ".gitbay", "issue-template-feature.md"),
		[]byte("## Motivation\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a\n"), 0o644)
	mustGit(t, dir, env, "checkout", "-q", "-b", "main")
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, env, "commit", "-q", "-m", "base")
	mustGit(t, dir, env, "push", "-q", "origin", "main")
	mustGit(t, dir, env, "checkout", "-q", "-b", "feat")
	os.WriteFile(filepath.Join(dir, "b.txt"), []byte("b\n"), 0o644)
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, env, "commit", "-q", "-m", "feat")
	mustGit(t, dir, env, "push", "-q", "origin", "feat")
	if _, _, code := inst.ssh(t, aliceKey, "", "mr", "create", "alice/app",
		"--source", "feat", "--target", "main", "--title", "'feat'"); code != 0 {
		t.Fatal("mr create failed")
	}
	for _, title := range []string{"'one'", "'two'"} {
		if _, _, code := inst.ssh(t, aliceKey, "", "issue", "create", "alice/app", "--title", title); code != 0 {
			t.Fatal("issue create failed")
		}
	}

	// Milestone lifecycle and validation.
	if _, errOut, code := inst.ssh(t, aliceKey, "", "milestone", "create", "alice/app", "v1.0",
		"--description", "'first release'", "--due", "2027-01-01"); code != 0 {
		t.Fatalf("milestone create: %s", errOut)
	}
	if _, _, code := inst.ssh(t, aliceKey, "", "milestone", "create", "alice/app", "v1.0"); code != 2 {
		t.Fatal("duplicate milestone accepted")
	}
	if _, errOut, code := inst.ssh(t, aliceKey, "", "milestone", "create", "alice/app", "v2.0", "--due", "soon"); code != 2 || !strings.Contains(errOut, "YYYY-MM-DD") {
		t.Fatalf("bad due accepted: %s", errOut)
	}
	if _, _, code := inst.ssh(t, bobKey, "", "milestone", "create", "alice/app", "sneaky"); code != 4 {
		t.Fatal("read-only user created a milestone")
	}

	// Attach: issue 1, MR 1; unknown title refused; clearing works.
	if _, errOut, code := inst.ssh(t, aliceKey, "", "issue", "milestone", "alice/app", "1", "v1.0"); code != 0 {
		t.Fatalf("issue milestone: %s", errOut)
	}
	if _, _, code := inst.ssh(t, aliceKey, "", "mr", "milestone", "alice/app", "1", "v1.0"); code != 0 {
		t.Fatal("mr milestone failed")
	}
	if _, _, code := inst.ssh(t, aliceKey, "", "issue", "milestone", "alice/app", "2", "v9.9"); code != 3 {
		t.Fatal("unknown milestone accepted")
	}
	out, _, _ := inst.ssh(t, aliceKey, "", "issue", "show", "alice/app", "1", "--json")
	if !strings.Contains(out, `"milestone":"v1.0"`) {
		t.Fatalf("issue show milestone: %s", out)
	}
	out, _, _ = inst.ssh(t, aliceKey, "", "mr", "show", "alice/app", "1", "--json")
	if !strings.Contains(out, `"milestone":"v1.0"`) {
		t.Fatalf("mr show milestone: %s", out)
	}

	// Progress: 2 open (issue 1 + MR 1); closing the issue moves it.
	out, _, _ = inst.ssh(t, aliceKey, "", "milestone", "list", "alice/app", "--json")
	if !strings.Contains(out, `"open":2`) || !strings.Contains(out, `"due":"2027-01-01"`) {
		t.Fatalf("milestone list: %s", out)
	}
	if _, _, code := inst.ssh(t, aliceKey, "", "issue", "close", "alice/app", "1"); code != 0 {
		t.Fatal("issue close failed")
	}
	out, _, _ = inst.ssh(t, aliceKey, "", "milestone", "list", "alice/app", "--json")
	if !strings.Contains(out, `"open":1`) || !strings.Contains(out, `"closed":1`) {
		t.Fatalf("milestone progress: %s", out)
	}

	// Close/reopen; closed milestones leave the default list.
	if _, _, code := inst.ssh(t, aliceKey, "", "milestone", "close", "alice/app", "v1.0"); code != 0 {
		t.Fatal("milestone close failed")
	}
	out, _, _ = inst.ssh(t, aliceKey, "", "milestone", "list", "alice/app")
	if strings.Contains(out, "v1.0") {
		t.Fatal("closed milestone in open list")
	}
	if _, _, code := inst.ssh(t, aliceKey, "", "milestone", "reopen", "alice/app", "v1.0"); code != 0 {
		t.Fatal("milestone reopen failed")
	}

	// Web: milestones page with progress; issue page links the milestone.
	status, body := inst.get(t, "/alice/app/milestones")
	if status != 200 || !strings.Contains(body, "v1.0") || !strings.Contains(body, "50%") ||
		!strings.Contains(body, "first release") {
		t.Fatalf("milestones page: %d\n%s", status, body)
	}
	_, body = inst.get(t, "/alice/app/issues/1")
	if !strings.Contains(body, ">v1.0</a>") {
		t.Fatal("issue page missing milestone link")
	}

	// Templates over SSH.
	out, _, _ = inst.ssh(t, aliceKey, "", "issue", "templates", "alice/app", "--json")
	if !strings.Contains(out, `"name":"issue-template.md"`) ||
		!strings.Contains(out, "Steps to reproduce") ||
		!strings.Contains(out, `"name":"issue-template-feature.md"`) {
		t.Fatalf("issue templates: %s", out)
	}

	// Templates on the web form (login required).
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
	browser := newBrowser(t)
	browserGet(t, browser, inst.base()+env2.Data.URL[strings.Index(env2.Data.URL, "/login"):])
	_, body = browserGet(t, browser, inst.base()+"/alice/app/issues/new")
	if !strings.Contains(body, "Steps to reproduce") || !strings.Contains(body, "issue-template-feature.md") {
		t.Fatalf("issue form not prefilled:\n%s", body)
	}
	_, body = browserGet(t, browser, inst.base()+"/alice/app/issues/new?template=issue-template-feature.md")
	if !strings.Contains(body, "Motivation") {
		t.Fatal("template switch failed")
	}
}
