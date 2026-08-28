package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The three reads that were reachable only in a browser: history at a
// ref, an archive, and the public listing. Each existed as a web route
// whose handler went around the registry, so no other surface had them.
func TestWebOnlyReadsAreCommands(t *testing.T) {
	inst := startInstance(t)
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

	os.WriteFile(filepath.Join(dir, "f.txt"), []byte("main line\n"), 0o644)
	mustGit(t, dir, env, "checkout", "-q", "-b", "main")
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, env, "commit", "-q", "-m", "on main")
	mustGit(t, dir, env, "push", "-q", "origin", "main")

	mustGit(t, dir, env, "checkout", "-q", "-b", "side")
	os.WriteFile(filepath.Join(dir, "f.txt"), []byte("side line\n"), 0o644)
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, env, "commit", "-q", "-m", "only on side")
	mustGit(t, dir, env, "push", "-q", "origin", "side")

	// --- history at a ref ---
	out, errOut, code := inst.ssh(t, aliceKey, "", "repo", "log", "alice/app", "--json")
	if code != 0 {
		t.Fatalf("repo log: %s", errOut)
	}
	if strings.Contains(out, "only on side") {
		t.Error("the default branch log carried a side-branch commit")
	}
	out, errOut, code = inst.ssh(t, aliceKey, "", "repo", "log", "alice/app", "--ref", "side", "--json")
	if code != 0 {
		t.Fatalf("repo log --ref: %s", errOut)
	}
	if !strings.Contains(out, "only on side") {
		t.Errorf("log at a ref missed its commit: %s", out)
	}
	if _, _, code := inst.ssh(t, aliceKey, "", "repo", "log", "alice/app", "--ref", "nope"); code == 0 {
		t.Error("an unknown ref resolved")
	}

	// --- archive ---
	out, errOut, code = inst.ssh(t, aliceKey, "", "repo", "download", "alice/app")
	if code != 0 {
		t.Fatalf("repo download: %s", errOut)
	}
	// A gzip stream, not an error page: magic bytes 0x1f 0x8b.
	if len(out) < 2 || out[0] != 0x1f || out[1] != 0x8b {
		t.Errorf("repo download did not produce gzip (%d bytes)", len(out))
	}
	if _, _, code := inst.ssh(t, aliceKey, "", "repo", "download", "alice/app", "--ref", "nope"); code == 0 {
		t.Error("archived an unknown ref")
	}

	// --- the public listing ---
	if _, _, code := inst.ssh(t, aliceKey, "", "repo", "create", "alice/hidden", "--private"); code != 0 {
		t.Fatal("private repo create")
	}
	out, errOut, code = inst.ssh(t, bobKey, "", "explore", "--json")
	if code != 0 {
		t.Fatalf("explore: %s", errOut)
	}
	if !strings.Contains(out, `"path":"alice/app"`) {
		t.Errorf("explore missed a public repository: %s", out)
	}
	if strings.Contains(out, "alice/hidden") {
		t.Errorf("explore leaked a private repository: %s", out)
	}

	// Paginated like the other listings.
	out, _, code = inst.ssh(t, bobKey, "", "explore", "--limit", "1", "--json")
	if code != 0 || !strings.Contains(out, `"items"`) {
		t.Fatalf("explore --limit: %s", out)
	}
}
