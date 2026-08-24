package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWebAutolinks(t *testing.T) {
	inst := startInstance(t)
	aliceKey := inst.newKey(t, "alice")
	bobKey := inst.newKey(t, "bob")
	inst.admin(t, "admin", "user", "create", "alice",
		"--key", aliceKey+".pub", "--email", "alice@example.test", "--verified")
	inst.admin(t, "admin", "user", "create", "bob", "--key", bobKey+".pub")

	// Public repo with an issue, an MR, and a private repo with an issue.
	if _, errOut, code := inst.ssh(t, aliceKey, "", "repo", "create", "alice/app"); code != 0 {
		t.Fatalf("repo create: %s", errOut)
	}
	if _, _, code := inst.ssh(t, aliceKey, "", "repo", "create", "alice/secret", "--private"); code != 0 {
		t.Fatal("private repo create failed")
	}
	if _, _, code := inst.ssh(t, aliceKey, "", "issue", "create", "alice/secret", "--title", "'hidden'"); code != 0 {
		t.Fatal("private issue create failed")
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
	os.WriteFile(filepath.Join(dir, "b.txt"), []byte("b\n"), 0o644)
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, env, "commit", "-q", "-m", "feat")
	mustGit(t, dir, env, "push", "-q", "origin", "feat")
	if _, errOut, code := inst.ssh(t, aliceKey, "", "mr", "create", "alice/app",
		"--source", "feat", "--target", "main", "--title", "'feat'"); code != 0 {
		t.Fatalf("mr create: %s", errOut)
	}
	if _, _, code := inst.ssh(t, aliceKey, "", "issue", "create", "alice/app", "--title", "'first'"); code != 0 {
		t.Fatal("issue 1 create failed")
	}
	if _, _, code := inst.ssh(t, aliceKey, "", "issue", "create", "alice/app",
		"--title", "'refs'", "--body",
		"'see #1 and !1 and alice/app#1 and @bob, not #99, not alice/secret#1, not `#1` in code'"); code != 0 {
		t.Fatal("issue 2 create failed")
	}

	status, body := inst.get(t, "/alice/app/issues/2")
	if status != 200 {
		t.Fatalf("issue page: %d", status)
	}
	for _, want := range []string{
		`<a href="/alice/app/issues/1" class="xref">#1</a>`,
		`<a href="/alice/app/mrs/1" class="xref">!1</a>`,
		`<a href="/alice/app/issues/1" class="xref">alice/app#1</a>`,
		`<a href="/bob" class="xref">@bob</a>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q on issue page", want)
		}
	}
	if strings.Contains(body, "issues/99") {
		t.Error("nonexistent issue got linked")
	}
	// The private repo must stay plain text for anonymous viewers: a link
	// would confirm it exists.
	if strings.Contains(body, `href="/alice/secret`) {
		t.Error("private repo reference leaked as a link")
	}
	if !strings.Contains(body, "alice/secret#1") {
		t.Error("private repo reference text missing entirely")
	}
	if !strings.Contains(body, "<code>#1</code>") {
		t.Error("code-span reference was rewritten")
	}

	// Comments on MR pages get the same treatment.
	if _, _, code := inst.ssh(t, bobKey, "", "mr", "comment", "alice/app", "1", "--message", "'closes #2'"); code != 0 {
		t.Fatal("mr comment failed")
	}
	_, body = inst.get(t, "/alice/app/mrs/1")
	if !strings.Contains(body, `<a href="/alice/app/issues/2" class="xref">#2</a>`) {
		t.Error("MR comment reference not linked")
	}
}
