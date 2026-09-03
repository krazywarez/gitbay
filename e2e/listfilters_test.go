package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// issue list and mr list narrow by label, assignee, author and
// milestone, and the web lists take the same names as query parameters.
func TestListFilters(t *testing.T) {
	inst := startInstance(t)
	aliceKey := inst.newKey(t, "alice")
	bobKey := inst.newKey(t, "bob")
	inst.admin(t, "admin", "user", "create", "alice", "--key", aliceKey+".pub")
	inst.admin(t, "admin", "user", "create", "bob", "--key", bobKey+".pub")
	must := func(key string, args ...string) {
		t.Helper()
		if _, errOut, code := inst.ssh(t, key, "", args...); code != 0 {
			t.Fatalf("%v: %s", args, errOut)
		}
	}
	must(aliceKey, "repo", "create", "alice/app")
	must(aliceKey, "repo", "access", "grant", "alice/app", "bob", "write")
	must(aliceKey, "milestone", "create", "alice/app", "v1")
	must(aliceKey, "issue", "create", "alice/app", "--title", "one")   // #1 alice, bug, assigned bob, v1
	must(bobKey, "issue", "create", "alice/app", "--title", "two")     // #2 bob, docs
	must(aliceKey, "issue", "create", "alice/app", "--title", "three") // #3 alice, closed, bug
	must(aliceKey, "issue", "label", "alice/app", "1", "--add", "bug")
	must(aliceKey, "issue", "label", "alice/app", "3", "--add", "bug")
	must(aliceKey, "issue", "label", "alice/app", "2", "--add", "docs")
	must(aliceKey, "issue", "assign", "alice/app", "1", "--add", "bob")
	must(aliceKey, "issue", "milestone", "alice/app", "1", "v1")
	must(aliceKey, "issue", "close", "alice/app", "3")

	list := func(args ...string) string {
		t.Helper()
		out, errOut, code := inst.ssh(t, aliceKey, "", append([]string{"issue", "list", "alice/app"}, args...)...)
		if code != 0 {
			t.Fatalf("issue list %v: %s", args, errOut)
		}
		return out
	}
	nums := func(out string) string {
		var n []string
		for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
			if line != "" {
				n = append(n, strings.Fields(line)[0])
			}
		}
		return strings.Join(n, " ")
	}
	cases := []struct{ args, want string }{
		{"--label bug", "#1"},
		{"--label bug --state all", "#3 #1"},
		{"--label docs", "#2"},
		{"--assignee bob", "#1"},
		{"--author bob", "#2"},
		{"--author alice --state all", "#3 #1"},
		{"--milestone v1", "#1"},
		{"--milestone none", "#2"},
		{"--label bug --milestone none", ""},
	}
	for _, c := range cases {
		if got := nums(list(strings.Fields(c.args)...)); got != c.want {
			t.Errorf("issue list %s: got %q, want %q", c.args, got, c.want)
		}
	}
	if _, _, code := inst.ssh(t, aliceKey, "", "issue", "list", "alice/app", "--label"); code != 2 {
		t.Fatal("dangling --label accepted")
	}

	// Merge requests: author and milestone.
	must(aliceKey, "repo", "create", "alice/lib")
	env := inst.gitEnv(aliceKey)
	work := t.TempDir()
	mustGit(t, work, env, "clone", inst.sshURL("alice/lib"), "w")
	dir := filepath.Join(work, "w")
	os.WriteFile(filepath.Join(dir, "base.txt"), []byte("base\n"), 0o644)
	mustGit(t, dir, env, "checkout", "-q", "-b", "main")
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, env, "commit", "-q", "-m", "base")
	mustGit(t, dir, env, "push", "-q", "origin", "main")
	for _, b := range []string{"f1", "f2"} {
		mustGit(t, dir, env, "checkout", "-q", "-b", b, "main")
		os.WriteFile(filepath.Join(dir, b+".txt"), []byte(b+"\n"), 0o644)
		mustGit(t, dir, env, "add", ".")
		mustGit(t, dir, env, "commit", "-q", "-m", b)
		mustGit(t, dir, env, "push", "-q", "origin", b)
	}
	must(aliceKey, "mr", "create", "alice/lib", "--source", "f1", "--target", "main", "--title", "a")
	must(aliceKey, "repo", "access", "grant", "alice/lib", "bob", "write")
	must(bobKey, "mr", "create", "alice/lib", "--source", "f2", "--target", "main", "--title", "b")
	must(aliceKey, "milestone", "create", "alice/lib", "v1")
	must(aliceKey, "mr", "milestone", "alice/lib", "2", "v1")
	mrl := func(args ...string) string {
		out, _, _ := inst.ssh(t, aliceKey, "", append([]string{"mr", "list", "alice/lib"}, args...)...)
		return nums(out)
	}
	if got := mrl("--author", "bob"); got != "!2" {
		t.Errorf("mr list --author bob: %q", got)
	}
	if got := mrl("--milestone", "v1"); got != "!2" {
		t.Errorf("mr list --milestone v1: %q", got)
	}
	if got := mrl("--milestone", "none"); got != "!1" {
		t.Errorf("mr list --milestone none: %q", got)
	}

	// The web takes the same names.
	if _, body := inst.get(t, "/alice/app/issues?assignee=bob&state=all"); !strings.Contains(body, ">one<") || strings.Contains(body, ">two<") || !strings.Contains(body, "assignee: <b>bob</b>") {
		t.Fatalf("web assignee filter:\n%s", body)
	}
	if _, body := inst.get(t, "/alice/app/issues?label=bug&state=all&author=alice"); !strings.Contains(body, ">three<") || strings.Contains(body, ">two<") || !strings.Contains(body, `href="?author=alice&amp;state=all"`) {
		t.Fatalf("web label+author filter with clear link:\n%s", body)
	}
	if _, body := inst.get(t, "/alice/lib/mrs?author=bob"); !strings.Contains(body, ">b<") || strings.Contains(body, ">a<") {
		t.Fatalf("web mr author filter:\n%s", body)
	}
}
