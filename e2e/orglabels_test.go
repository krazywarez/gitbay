package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// An org's labels and milestones reach every repository under it; a
// commit in one repository closes an issue in another; the org pages
// answer members and outsiders as their access allows.
func TestOrgLabelsMilestonesAndCrossRepoCloses(t *testing.T) {
	inst := startInstance(t)
	aliceKey := inst.newKey(t, "alice")
	carolKey := inst.newKey(t, "carol")
	inst.admin(t, "admin", "user", "create", "alice", "--key", aliceKey+".pub", "--email", "alice@example.test", "--verified")
	inst.admin(t, "admin", "user", "create", "carol", "--key", carolKey+".pub", "--email", "carol@example.test", "--verified")
	must := func(key string, args ...string) string {
		t.Helper()
		out, errOut, code := inst.ssh(t, key, "", args...)
		if code != 0 {
			t.Fatalf("%v: exit %d %s", args, code, errOut)
		}
		return out
	}
	must(aliceKey, "org", "create", "acme")
	must(aliceKey, "repo", "create", "acme/lib")
	must(aliceKey, "repo", "create", "acme/widget", "--private")
	must(aliceKey, "issue", "create", "acme/lib", "--title", "'lib one'")
	must(aliceKey, "issue", "create", "acme/widget", "--title", "'widget one'")

	// Repo labels in both, then the org set folds them in.
	must(aliceKey, "issue", "label", "acme/lib", "1", "--add", "bug")
	must(aliceKey, "issue", "label", "acme/widget", "1", "--add", "bug")
	out := must(aliceKey, "org", "label", "set", "acme", "bug", "--color", "ff0000", "--json")
	if !strings.Contains(out, `"folded":2`) {
		t.Fatalf("org label set: %s", out)
	}
	out = must(aliceKey, "label", "list", "acme/lib", "--json")
	if !strings.Contains(out, `"org":true`) || !strings.Contains(out, `"issues":2`) {
		t.Fatalf("lib label list: %s", out)
	}
	if _, errOut, code := inst.ssh(t, aliceKey, "", "label", "set", "acme/lib", "bug"); code == 0 || !strings.Contains(errOut, "org label set acme bug") {
		t.Fatalf("repo label set over org name: exit %d %s", code, errOut)
	}

	// An org milestone attaches from both repositories and counts across.
	must(aliceKey, "org", "milestone", "create", "acme", "v1", "--due", "2027-01-01")
	must(aliceKey, "issue", "milestone", "acme/lib", "1", "v1")
	must(aliceKey, "issue", "milestone", "acme/widget", "1", "v1")
	out = must(aliceKey, "org", "milestone", "list", "acme", "--json")
	if !strings.Contains(out, `"open":2`) {
		t.Fatalf("org milestone list: %s", out)
	}
	out = must(aliceKey, "issue", "list", "acme/lib", "--milestone", "v1", "--json")
	if !strings.Contains(out, `"number":1`) {
		t.Fatalf("issue list filtered by org milestone: %s", out)
	}

	// A push to acme/lib closes acme/widget#1 and leaves a comment there.
	work := t.TempDir()
	env := inst.gitEnv(aliceKey)
	mustGit(t, work, env, "clone", inst.sshURL("acme/lib"), "w")
	dir := filepath.Join(work, "w")
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a\n"), 0o644)
	mustGit(t, dir, env, "checkout", "-q", "-b", "main")
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, env, "commit", "-q", "-m", "fix the widget\n\nCloses acme/widget#1")
	mustGit(t, dir, env, "push", "-q", "origin", "main")
	out = must(aliceKey, "issue", "show", "acme/widget", "1", "--json")
	if !strings.Contains(out, `"state":"closed"`) || !strings.Contains(out, "](/acme/lib/commit/") {
		t.Fatalf("widget#1 after cross-repo close: %s", out)
	}
	out = must(aliceKey, "org", "milestone", "list", "acme", "--json")
	if !strings.Contains(out, `"open":1`) || !strings.Contains(out, `"closed":1`) {
		t.Fatalf("org milestone progress after close: %s", out)
	}

	// carol is outside: she reads the org pages because acme/lib is public,
	// and the counts stop at it.
	out = must(carolKey, "org", "milestone", "list", "acme", "--json")
	if !strings.Contains(out, `"open":1`) || !strings.Contains(out, `"closed":0`) {
		t.Fatalf("outsider progress: %s", out)
	}
	if status, body := inst.get(t, "/acme/-/labels"); status != 200 || !strings.Contains(body, ">bug<") {
		t.Fatalf("org labels page: %d", status)
	}
	if status, body := inst.get(t, "/acme/-/milestones"); status != 200 || !strings.Contains(body, "v1") || !strings.Contains(body, "0 closed, 1 open") {
		t.Fatalf("org milestones page: %d\n%s", status, body)
	}
	if status, body := inst.get(t, "/acme/lib/labels"); status != 200 || !strings.Contains(body, `chip-neutral">org<`) {
		t.Fatalf("repo labels page lacks the org mark: %d", status)
	}
	// carol cannot close into the private repo from a repo she owns.
	must(carolKey, "repo", "create", "carol/own")
	must(aliceKey, "issue", "create", "acme/widget", "--title", "'widget two'")
	cwork := t.TempDir()
	cenv := inst.gitEnv(carolKey)
	mustGit(t, cwork, cenv, "clone", inst.sshURL("carol/own"), "w")
	cdir := filepath.Join(cwork, "w")
	os.WriteFile(filepath.Join(cdir, "a.txt"), []byte("a\n"), 0o644)
	mustGit(t, cdir, cenv, "checkout", "-q", "-b", "main")
	mustGit(t, cdir, cenv, "add", ".")
	mustGit(t, cdir, cenv, "commit", "-q", "-m", "sneaky\n\nCloses acme/widget#2")
	mustGit(t, cdir, cenv, "push", "-q", "origin", "main")
	out = must(aliceKey, "issue", "show", "acme/widget", "2", "--json")
	if !strings.Contains(out, `"state":"open"`) || strings.Contains(out, "sneaky") {
		t.Fatalf("outsider acted on a private repo's issue: %s", out)
	}
	// With the public repo gone private, the org pages are not found for
	// an anonymous reader.
	must(aliceKey, "repo", "settings", "visibility", "acme/lib", "private")
	if status, _ := inst.get(t, "/acme/-/labels"); status != 404 {
		t.Fatalf("private org labels page for anonymous: %d", status)
	}
}
