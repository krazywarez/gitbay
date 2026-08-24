package e2e

import (
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIssueMREditing(t *testing.T) {
	inst := startInstanceWith(t, "[web]\nmode = \"accounts\"\n")
	aliceKey := inst.newKey(t, "alice")
	bobKey := inst.newKey(t, "bob")
	eveKey := inst.newKey(t, "eve")
	inst.admin(t, "admin", "user", "create", "alice",
		"--key", aliceKey+".pub", "--email", "alice@example.test", "--verified")
	inst.admin(t, "admin", "user", "create", "bob", "--key", bobKey+".pub")
	inst.admin(t, "admin", "user", "create", "eve", "--key", eveKey+".pub")

	if _, _, code := inst.ssh(t, aliceKey, "", "repo", "create", "alice/app"); code != 0 {
		t.Fatal("repo create failed")
	}
	// bob authors an issue (no write access); eve is an outsider.
	if _, _, code := inst.ssh(t, bobKey, "", "issue", "create", "alice/app", "--title", "'typo titel'", "--body", "'first'"); code != 0 {
		t.Fatal("issue create failed")
	}

	// SSH edit: the author may fix it; an outsider may not; empty titles
	// are refused; write access (alice) may edit someone else's.
	if _, errOut, code := inst.ssh(t, bobKey, "", "issue", "edit", "alice/app", "1", "--title", "'typo title'"); code != 0 {
		t.Fatalf("author edit: %s", errOut)
	}
	if _, _, code := inst.ssh(t, eveKey, "", "issue", "edit", "alice/app", "1", "--title", "'hax'"); code != 4 {
		t.Fatal("outsider edited an issue")
	}
	if _, _, code := inst.ssh(t, bobKey, "", "issue", "edit", "alice/app", "1", "--title", "''"); code != 2 {
		t.Fatal("empty title accepted")
	}
	if _, _, code := inst.ssh(t, aliceKey, "", "issue", "edit", "alice/app", "1", "--body", "'rewritten'"); code != 0 {
		t.Fatal("write-access edit failed")
	}
	out, _, _ := inst.ssh(t, aliceKey, "", "issue", "show", "alice/app", "1", "--json")
	if !strings.Contains(out, "typo title") || !strings.Contains(out, "rewritten") {
		t.Fatalf("edits not applied: %s", out)
	}

	// MR edit over SSH.
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
	os.WriteFile(filepath.Join(dir, "b.txt"), []byte("b\n"), 0o644)
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, env, "commit", "-q", "-m", "feat")
	mustGit(t, dir, env, "push", "-q", "origin", "feat")
	if _, _, code := inst.ssh(t, aliceKey, "", "mr", "create", "alice/app",
		"--source", "feat", "--target", "main", "--title", "'draft'"); code != 0 {
		t.Fatal("mr create failed")
	}
	if _, _, code := inst.ssh(t, aliceKey, "", "mr", "edit", "alice/app", "1", "--title", "'ready'"); code != 0 {
		t.Fatal("mr edit failed")
	}
	if out, _, _ := inst.ssh(t, aliceKey, "", "mr", "show", "alice/app", "1", "--json"); !strings.Contains(out, "ready") {
		t.Fatalf("mr edit not applied: %s", out)
	}

	// Web edit: form appears for the authorized viewer, POST applies, and
	// with write access the label set is replaced.
	out, _, code := inst.ssh(t, aliceKey, "", "web", "login", "--json")
	if code != 0 {
		t.Fatal("web login failed")
	}
	var env2 struct {
		Data struct {
			URL string `json:"url"`
		} `json:"data"`
	}
	json.Unmarshal([]byte(out), &env2)
	browser := newBrowser(t)
	browserGet(t, browser, inst.base()+env2.Data.URL[strings.Index(env2.Data.URL, "/login"):])
	_, body := browserGet(t, browser, inst.base()+"/alice/app/issues/1")
	if !strings.Contains(body, "/alice/app/issues/1/edit") {
		t.Fatal("edit form missing for authorized viewer")
	}
	if status, _ := browserPost(t, browser, inst.base()+"/alice/app/issues/1/edit",
		url.Values{"title": {"web-edited"}, "body": {"web body"}, "labels": {"bug"}}); status != 200 {
		t.Fatal("web issue edit failed")
	}
	out, _, _ = inst.ssh(t, aliceKey, "", "issue", "show", "alice/app", "1", "--json")
	if !strings.Contains(out, "web-edited") || !strings.Contains(out, `"labels":["bug"]`) {
		t.Fatalf("web edit not applied: %s", out)
	}
	if status, _ := browserPost(t, browser, inst.base()+"/alice/app/mrs/1/edit",
		url.Values{"title": {"web-mr"}, "body": {"mb"}}); status != 200 {
		t.Fatal("web mr edit failed")
	}
	// Anonymous view shows no edit affordance.
	_, body = inst.get(t, "/alice/app/issues/1")
	if strings.Contains(body, "/issues/1/edit") {
		t.Fatal("edit form leaked to anonymous viewer")
	}
}
