package e2e

import (
	"os"
	"os/exec"
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

// A runner host holds a key added with --scope runner: it can claim and
// report builds and clone what it builds, and nothing else. Neither the
// same account's full key nor the runner key reaches the wrong side, so a
// key stolen from a build step cannot administer the instance (#92).
func TestRunnerScopedKey(t *testing.T) {
	inst := startInstance(t)
	aliceKey := inst.newKey(t, "alice")
	inst.admin(t, "admin", "user", "create", "alice", "--key", aliceKey+".pub")
	if _, errOut, code := inst.ssh(t, aliceKey, "", "repo", "create", "alice/app"); code != 0 {
		t.Fatalf("repo create: %s", errOut)
	}

	// ci is an ordinary account. Its runner key is self-added.
	ciKey := inst.newKey(t, "ci")
	inst.admin(t, "admin", "user", "create", "ci", "--key", ciKey+".pub")
	runnerKey := inst.newKey(t, "ci-runner")
	pub, _ := os.ReadFile(runnerKey + ".pub")
	if _, errOut, code := inst.ssh(t, ciKey, string(pub), "keys", "add", "--scope", "runner"); code != 0 {
		t.Fatalf("keys add --scope runner: %s", errOut)
	}

	// The runner key claims (nothing is queued, which is a success).
	out, errOut, code := inst.ssh(t, runnerKey, "", "runner", "next", "--json")
	if code != 0 || !strings.Contains(out, "{}") {
		t.Fatalf("runner key cannot claim: exit %d\n%s%s", code, out, errOut)
	}
	// The runner key reaches no other command, whoami included.
	for _, argv := range [][]string{{"whoami"}, {"repo", "create", "ci/evil"}, {"admin", "user", "list"}} {
		if _, _, code := inst.ssh(t, runnerKey, "", argv...); code != 4 {
			t.Fatalf("runner key ran %v: exit %d, want 4", argv, code)
		}
	}
	// The account's full key is not a runner.
	if _, _, code := inst.ssh(t, ciKey, "", "runner", "next"); code != 4 {
		t.Fatalf("full key of a non-admin claimed a build: exit %d, want 4", code)
	}

	// Git: the runner key clones and cannot push.
	work := t.TempDir()
	env := inst.gitEnv(runnerKey)
	mustGit(t, work, env, "clone", "-q", inst.sshURL("alice/app"), "w")
	dir := filepath.Join(work, "w")
	os.WriteFile(filepath.Join(dir, "f"), []byte("x\n"), 0o644)
	mustGit(t, dir, env, "checkout", "-q", "-b", "main")
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, env, "commit", "-q", "-m", "x")
	push := exec.Command("git", "push", "-q", "origin", "main")
	push.Dir, push.Env = dir, env
	if pushOut, err := push.CombinedOutput(); err == nil || !strings.Contains(string(pushOut), "scope") {
		t.Fatalf("runner key pushed, or was refused for another reason: err=%v\n%s", err, pushOut)
	}
}
