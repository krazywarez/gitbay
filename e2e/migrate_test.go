package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAccountMigration(t *testing.T) {
	src := startInstance(t)
	dst := startInstance(t)
	key := src.newKey(t, "alice") // one identity, both instances
	src.admin(t, "admin", "user", "create", "alice", "--key", key+".pub")
	dst.admin(t, "admin", "user", "create", "alice", "--key", key+".pub")

	// Source: profile, a repo with settings/topics, an issue thread, an MR.
	if _, _, code := src.ssh(t, key, "", "profile", "set", "--description", "'tinkerer'", "--website", "https://alice.example"); code != 0 {
		t.Fatal("profile set failed")
	}
	if _, _, code := src.ssh(t, key, "", "repo", "create", "alice/tool", "--description", "'a fine tool'"); code != 0 {
		t.Fatal("repo create failed")
	}
	if _, _, code := src.ssh(t, key, "", "repo", "create", "alice/notes", "--private"); code != 0 {
		t.Fatal("private repo create failed")
	}
	src.ssh(t, key, "", "repo", "topics", "add", "alice/tool", "go", "cli")
	work := t.TempDir()
	env := src.gitEnv(key)
	mustGit(t, work, env, "clone", src.sshURL("alice/tool"), "w")
	dir := filepath.Join(work, "w")
	os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o644)
	mustGit(t, dir, env, "checkout", "-q", "-b", "main")
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, env, "commit", "-q", "-m", "base")
	mustGit(t, dir, env, "push", "-q", "origin", "main")
	head := strings.TrimSpace(mustGit(t, dir, env, "rev-parse", "HEAD"))
	src.ssh(t, key, "", "repo", "settings", "protect", "alice/tool", "main")
	src.ssh(t, key, "", "repo", "settings", "require-signed", "alice/tool", "on")
	if _, _, code := src.ssh(t, key, "", "issue", "create", "alice/tool", "--title", "'sharpen it'", "--body", "'too dull'"); code != 0 {
		t.Fatal("issue create failed")
	}
	src.ssh(t, key, "", "issue", "label", "alice/tool", "1", "--add", "bug")
	src.ssh(t, key, "", "issue", "comment", "alice/tool", "1", "--message", "'on it'")
	src.ssh(t, key, "", "issue", "close", "alice/tool", "1")

	// Export from the source, replay on the target.
	bundleOut, errOut, code := src.ssh(t, key, "", "account", "export")
	if code != 0 {
		t.Fatalf("export: %s", errOut)
	}
	if strings.Contains(bundleOut, "ssh-ed25519") || strings.Contains(bundleOut, "PRIVATE") {
		t.Fatal("bundle carries key material")
	}
	out, errOut, code := dst.ssh(t, key, bundleOut, "account", "import-bundle", "--source", "old.example")
	if code != 0 {
		t.Fatalf("import-bundle: %s", errOut)
	}
	if !strings.Contains(out, "imported 2 repos, 1 issues, 0 MRs, 1 comments") ||
		!strings.Contains(out, "require-signed alice/tool on") ||
		!strings.Contains(out, "protect alice/tool main") {
		t.Fatalf("import summary: %s", out)
	}

	// Metadata arrived: issue with state, label, comment, attribution;
	// topics, description, visibility.
	out, _, _ = dst.ssh(t, key, "", "issue", "show", "alice/tool", "1", "--json")
	if !strings.Contains(out, "sharpen it") || !strings.Contains(out, `"state":"closed"`) ||
		!strings.Contains(out, `"labels":["bug"]`) || !strings.Contains(out, "on it") ||
		!strings.Contains(out, "migrated issue old.example#1") {
		t.Fatalf("migrated issue: %s", out)
	}
	out, _, _ = dst.ssh(t, key, "", "repo", "show", "alice/tool", "--json")
	if !strings.Contains(out, `"topics":["cli","go"]`) || !strings.Contains(out, "a fine tool") {
		t.Fatalf("migrated repo: %s", out)
	}
	out, _, _ = dst.ssh(t, key, "", "repo", "show", "alice/notes", "--json")
	if !strings.Contains(out, `"visibility":"private"`) {
		t.Fatalf("private visibility lost: %s", out)
	}
	// Deferred policies are NOT applied yet — the push must succeed.
	out, _, _ = dst.ssh(t, key, "", "repo", "settings", "show", "alice/tool", "--json")
	if strings.Contains(out, `"require_signed_commits":true`) || strings.Contains(out, "protected") {
		t.Fatalf("push-blocking settings applied early: %s", out)
	}

	// Git data client-side: clone from source, push to target (unsigned
	// history lands because the policy is deferred).
	mirror := t.TempDir()
	mustGit(t, mirror, env, "clone", "-q", "--mirror", src.sshURL("alice/tool"), "m")
	mdir := filepath.Join(mirror, "m")
	mustGit(t, mdir, env, "push", "-q", dst.sshURL("alice/tool"), "+refs/heads/*:refs/heads/*", "+refs/tags/*:refs/tags/*")
	out, _, _ = dst.ssh(t, key, "", "repo", "log", "alice/tool", "--json")
	if !strings.Contains(out, head) {
		t.Fatalf("git data missing on target: %s", out)
	}

	// Resume: a second replay imports nothing twice.
	out, _, code = dst.ssh(t, key, bundleOut, "account", "import-bundle", "--source", "old.example")
	if code != 0 || !strings.Contains(out, "imported 0 repos, 0 issues, 0 MRs, 0 comments (1 already present)") {
		t.Fatalf("re-import: %s", out)
	}
}
