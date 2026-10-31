package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The push-time secret scan runs in the real pre-receive hook, against
// objects still in quarantine: warn accepts and tells the pusher where,
// block refuses, and an allowed path is let through (#339).
func TestSecretScanOnPush(t *testing.T) {
	t.Parallel()
	inst := startInstance(t)
	aliceKey := inst.newKey(t, "alice")
	inst.admin(t, "admin", "user", "create", "alice", "--key", aliceKey+".pub")
	if _, errOut, code := inst.ssh(t, aliceKey, "", "repo", "create", "alice/app"); code != 0 {
		t.Fatalf("repo create: %s", errOut)
	}
	work := t.TempDir()
	env := inst.gitEnv(aliceKey)
	mustGit(t, work, env, "clone", "-q", inst.sshURL("alice/app"), "w")
	dir := filepath.Join(work, "w")
	mustGit(t, dir, env, "checkout", "-q", "-b", "main")
	write := func(name, body string) {
		t.Helper()
		p := filepath.Join(dir, name)
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		mustGit(t, dir, env, "add", ".")
		mustGit(t, dir, env, "commit", "-q", "-m", name)
	}
	// Assembled, so this file is not itself a finding.
	token := "gh" + "p_" + strings.Repeat("aB3dE5", 6)

	write("README.md", "# app\n")
	write("deploy.env", "x=1\nGITHUB_TOKEN="+token+"\n")
	out, code := gitRun(t, dir, env, "push", "origin", "main")
	if code != 0 || !strings.Contains(out, "deploy.env:2 github-token") || !strings.Contains(out, "the push was accepted") {
		t.Fatalf("warn: %d\n%s", code, out)
	}
	if strings.Contains(out, token) {
		t.Fatalf("push output carries the token:\n%s", out)
	}

	if _, errOut, code := inst.ssh(t, aliceKey, "", "repo", "settings", "secret-scan", "alice/app", "block"); code != 0 {
		t.Fatalf("secret-scan block: %s", errOut)
	}
	write("testdata/fixture.env", token+"\n")
	if out, code := gitRun(t, dir, env, "push", "origin", "main"); code == 0 || !strings.Contains(out, "refused, 1 possible secret") {
		t.Fatalf("block: %d\n%s", code, out)
	}
	if _, errOut, code := inst.ssh(t, aliceKey, "", "repo", "settings", "secret-scan-allow", "alice/app", "'testdata/**'"); code != 0 {
		t.Fatalf("secret-scan-allow: %s", errOut)
	}
	if out, code := gitRun(t, dir, env, "push", "origin", "main"); code != 0 || strings.Contains(out, "secret scan") {
		t.Fatalf("allowed path: %d\n%s", code, out)
	}

	// Everything already pushed is in the history scan, allowlist applied.
	out, errOut, code := inst.ssh(t, aliceKey, "", "repo", "secret-scan", "alice/app", "--json")
	if code != 0 || !strings.Contains(out, `"path":"deploy.env"`) || strings.Contains(out, "testdata/") {
		t.Fatalf("history: %d %s\n%s", code, errOut, out)
	}
}
