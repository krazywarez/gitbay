package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A runner names the repositories it will take builds for. Without that, any
// runner claims whatever is next in the global queue, so a runner on a machine
// that should only build one project ends up executing every repository's
// steps — including those of a repository it has nothing to do with.
func TestRunnerNextScopedToRepos(t *testing.T) {
	inst := startInstance(t)
	aliceKey := inst.newKey(t, "alice")
	inst.admin(t, "admin", "user", "create", "alice", "--key", aliceKey+".pub")
	runnerKey := inst.newKey(t, "ci")
	inst.admin(t, "admin", "user", "create", "ci", "--key", runnerKey+".pub", "--admin")

	// Two repositories, each with a build queued. "other" is pushed first, so
	// an unscoped claim would take it.
	for _, name := range []string{"other", "site"} {
		if _, errOut, code := inst.ssh(t, aliceKey, "", "repo", "create", "alice/"+name); code != 0 {
			t.Fatalf("repo create %s: %s", name, errOut)
		}
		work := t.TempDir()
		env := inst.gitEnv(aliceKey)
		mustGit(t, work, env, "clone", inst.sshURL("alice/"+name), "w")
		dir := filepath.Join(work, "w")
		os.MkdirAll(filepath.Join(dir, ".gitbay"), 0o755)
		os.WriteFile(filepath.Join(dir, ".gitbay", "ci.yml"), []byte(
			"jobs:\n  "+name+":\n    steps:\n      - echo hi\n"), 0o644)
		mustGit(t, dir, env, "checkout", "-q", "-b", "main")
		mustGit(t, dir, env, "add", ".")
		mustGit(t, dir, env, "commit", "-q", "-m", "base")
		mustGit(t, dir, env, "push", "-q", "origin", "main")
	}

	// Scoped to alice/site: takes the site build, not the older other build.
	out, errOut, code := inst.ssh(t, runnerKey, "", "runner", "next", "alice/site", "--json")
	if code != 0 {
		t.Fatalf("runner next: %s", errOut)
	}
	if !strings.Contains(out, `"repo":"alice/site"`) {
		t.Fatalf("scoped claim took the wrong repo:\n%s", out)
	}

	// That scope is now empty, though alice/other is still pending.
	out, _, code = inst.ssh(t, runnerKey, "", "runner", "next", "alice/site", "--json")
	if code != 0 || strings.Contains(out, `"repo":`) {
		t.Fatalf("scoped claim took a build outside its scope:\n%s", out)
	}

	// An unscoped runner still takes it, so the default is unchanged.
	out, _, code = inst.ssh(t, runnerKey, "", "runner", "next", "--json")
	if code != 0 || !strings.Contains(out, `"repo":"alice/other"`) {
		t.Fatalf("unscoped claim did not take the remaining build:\n%s", out)
	}
}
