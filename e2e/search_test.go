package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSearch(t *testing.T) {
	inst := startInstance(t)
	aliceKey := inst.newKey(t, "alice")
	bobKey := inst.newKey(t, "bob")
	inst.admin(t, "admin", "user", "create", "alice",
		"--key", aliceKey+".pub", "--email", "alice@example.test", "--verified")
	inst.admin(t, "admin", "user", "create", "bob", "--key", bobKey+".pub")

	if _, errOut, code := inst.ssh(t, aliceKey, "", "repo", "create", "alice/webapp",
		"--description", "'a small web application'"); code != 0 {
		t.Fatalf("repo create: %s", errOut)
	}
	if _, _, code := inst.ssh(t, aliceKey, "", "repo", "topics", "add", "alice/webapp", "golang"); code != 0 {
		t.Fatal("topics add failed")
	}
	if _, _, code := inst.ssh(t, aliceKey, "", "repo", "create", "alice/secret", "--private",
		"--description", "'hidden things'"); code != 0 {
		t.Fatal("private repo create failed")
	}

	work := t.TempDir()
	env := inst.gitEnv(aliceKey)
	mustGit(t, work, env, "clone", inst.sshURL("alice/webapp"), "w")
	dir := filepath.Join(work, "w")
	os.WriteFile(filepath.Join(dir, "main.go"),
		[]byte("package main\n\nfunc Greet() string {\n\treturn \"hello, forge\"\n}\n"), 0o644)
	mustGit(t, dir, env, "checkout", "-q", "-b", "main")
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, env, "commit", "-q", "-m", "base")
	mustGit(t, dir, env, "push", "-q", "origin", "main")

	// repo search over SSH: by name, description, and topic; visibility
	// respected — bob never sees the private repo, alice does.
	out, _, code := inst.ssh(t, aliceKey, "", "repo", "search", "webapp")
	if code != 0 || !strings.Contains(out, "alice/webapp") {
		t.Fatalf("search by name: %s", out)
	}
	if out, _, _ = inst.ssh(t, bobKey, "", "repo", "search", "application"); !strings.Contains(out, "alice/webapp") {
		t.Fatalf("search by description: %s", out)
	}
	if out, _, _ = inst.ssh(t, bobKey, "", "repo", "search", "golang"); !strings.Contains(out, "alice/webapp") {
		t.Fatalf("search by topic: %s", out)
	}
	if out, _, _ = inst.ssh(t, bobKey, "", "repo", "search", "secret"); strings.Contains(out, "alice/secret") {
		t.Fatal("private repo leaked to bob's search")
	}
	if out, _, _ = inst.ssh(t, aliceKey, "", "repo", "search", "hidden"); !strings.Contains(out, "alice/secret") {
		t.Fatalf("owner's search missed private repo: %s", out)
	}
	if _, errOut, code := inst.ssh(t, aliceKey, "", "repo", "search", "x"); code != 2 || !strings.Contains(errOut, "2 to 200") {
		t.Fatalf("short query: exit %d, %s", code, errOut)
	}

	// repo grep over SSH: literal, case-insensitive, 404-parity on private.
	out, _, code = inst.ssh(t, aliceKey, "", "repo", "grep", "alice/webapp", "HELLO", "--json")
	if code != 0 || !strings.Contains(out, `"path":"main.go"`) || !strings.Contains(out, `"line":4`) {
		t.Fatalf("grep: %s", out)
	}
	if _, errOut, code := inst.ssh(t, bobKey, "", "repo", "grep", "alice/secret", "anything"); code != 3 || !strings.Contains(errOut, "not found") {
		t.Fatalf("private grep parity: exit %d, %s", code, errOut)
	}

	// Web: index filter respects the query and visibility.
	status, body := inst.get(t, "/?q=webapp")
	if status != 200 || !strings.Contains(body, "alice/webapp") {
		t.Fatalf("index filter: %d", status)
	}
	_, body = inst.get(t, "/?q=nomatchhere")
	if strings.Contains(body, "alice/webapp") {
		t.Fatal("index filter did not filter")
	}
	_, body = inst.get(t, "/?q=hidden")
	if strings.Contains(body, "secret") {
		t.Fatal("private repo leaked on index search")
	}

	// Web: repo code search with mark and blob line anchor.
	status, body = inst.get(t, "/alice/webapp/search?q=hello")
	if status != 200 || !strings.Contains(body, `<mark>hello</mark>`) ||
		!strings.Contains(body, `/alice/webapp/blob/main/main.go#L4">main.go:4</a>`) {
		t.Fatalf("repo search page: %d\n%s", status, body)
	}
	_, body = inst.get(t, "/alice/webapp/search?q=zzznothing")
	if !strings.Contains(body, "no matches") {
		t.Fatal("empty state missing")
	}
	_, body = inst.get(t, "/alice/webapp/search?q=x")
	if !strings.Contains(body, "2 to 200") {
		t.Fatal("short query error missing")
	}

	// Blob line numbers are linkable anchors for the search links.
	_, body = inst.get(t, "/alice/webapp/blob/main/main.go")
	if !strings.Contains(body, `id="L4"`) {
		t.Fatal("blob line anchors missing")
	}
}
