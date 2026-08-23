package e2e

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// gitEnv returns the environment for running the git client against the
// instance with the given key.
func (i *instance) gitEnv(key string) []string {
	sshCmd := fmt.Sprintf(
		"ssh -i %s -o IdentitiesOnly=yes -o StrictHostKeyChecking=no -o UserKnownHostsFile=%s -o BatchMode=yes",
		key, filepath.Join(i.sshDir, "known_hosts"))
	return append(os.Environ(),
		"GIT_SSH_COMMAND="+sshCmd,
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.test",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.test",
	)
}

func (i *instance) sshURL(repo string) string {
	return fmt.Sprintf("ssh://git@127.0.0.1:%d/%s.git", i.port, repo)
}

// git runs a git command; returns combined output and exit code.
func gitRun(t *testing.T, dir string, env []string, args ...string) (string, int) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return string(out), code
}

func mustGit(t *testing.T, dir string, env []string, args ...string) string {
	t.Helper()
	out, code := gitRun(t, dir, env, args...)
	if code != 0 {
		t.Fatalf("git %v failed (%d):\n%s", args, code, out)
	}
	return out
}

func TestGitOverSSH(t *testing.T) {
	inst := startInstance(t)

	aliceKey := inst.newKey(t, "alice")
	bobKey := inst.newKey(t, "bob")
	inst.admin(t, "admin", "user", "create", "alice", "--key", aliceKey+".pub")
	inst.admin(t, "admin", "user", "create", "bob", "--key", bobKey+".pub")

	// Alice creates a private repo over bare ssh.
	_, errOut, code := inst.ssh(t, aliceKey, "", "repo", "create", "alice/proj", "--private")
	if code != 0 {
		t.Fatalf("repo create: exit %d, %s", code, errOut)
	}

	// Alice clones (empty), commits, pushes.
	work := t.TempDir()
	aliceEnv := inst.gitEnv(aliceKey)
	mustGit(t, work, aliceEnv, "clone", inst.sshURL("alice/proj"), "proj")
	dir := filepath.Join(work, "proj")
	if err := os.WriteFile(filepath.Join(dir, "README"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustGit(t, dir, aliceEnv, "checkout", "-q", "-b", "main")
	mustGit(t, dir, aliceEnv, "add", "README")
	mustGit(t, dir, aliceEnv, "commit", "-q", "-m", "init")
	mustGit(t, dir, aliceEnv, "push", "-q", "origin", "main")

	// Bob is denied clone of the private repo, indistinguishable from
	// nonexistence.
	bobEnv := inst.gitEnv(bobKey)
	out, code := gitRun(t, t.TempDir(), bobEnv, "clone", inst.sshURL("alice/proj"), "proj")
	if code == 0 {
		t.Fatal("bob cloned a private repo without access")
	}
	if !strings.Contains(out, "repository not found") {
		t.Fatalf("denial should read as not-found, got:\n%s", out)
	}

	// Alice grants bob read; clone succeeds; push is denied.
	_, errOut, code = inst.ssh(t, aliceKey, "", "repo", "access", "grant", "alice/proj", "bob", "read")
	if code != 0 {
		t.Fatalf("access grant: exit %d, %s", code, errOut)
	}
	bobWork := t.TempDir()
	mustGit(t, bobWork, bobEnv, "clone", inst.sshURL("alice/proj"), "proj")
	bobDir := filepath.Join(bobWork, "proj")
	if err := os.WriteFile(filepath.Join(bobDir, "x"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustGit(t, bobDir, bobEnv, "add", "x")
	mustGit(t, bobDir, bobEnv, "commit", "-q", "-m", "bob")
	out, code = gitRun(t, bobDir, bobEnv, "push", "origin", "main")
	if code == 0 {
		t.Fatal("bob pushed with read-only access")
	}
	if !strings.Contains(out, "write access to alice/proj denied") {
		t.Fatalf("push denial message:\n%s", out)
	}

	// Alice protects main: force-push and deletion are refused by the hook,
	// normal pushes still work.
	_, errOut, code = inst.ssh(t, aliceKey, "", "repo", "settings", "protect", "alice/proj", "main")
	if code != 0 {
		t.Fatalf("protect: exit %d, %s", code, errOut)
	}

	mustGit(t, dir, aliceEnv, "commit", "-q", "--allow-empty", "-m", "second")
	mustGit(t, dir, aliceEnv, "push", "-q", "origin", "main")

	mustGit(t, dir, aliceEnv, "reset", "-q", "--hard", "HEAD~1")
	mustGit(t, dir, aliceEnv, "commit", "-q", "--allow-empty", "-m", "rewritten")
	out, code = gitRun(t, dir, aliceEnv, "push", "--force", "origin", "main")
	if code == 0 {
		t.Fatal("force-push to protected branch succeeded")
	}
	if !strings.Contains(out, "force-push refused") {
		t.Fatalf("force-push denial message:\n%s", out)
	}

	out, code = gitRun(t, dir, aliceEnv, "push", "origin", ":main")
	if code == 0 {
		t.Fatal("deletion of protected branch succeeded")
	}
	if !strings.Contains(out, "deletion refused") {
		t.Fatalf("deletion denial message:\n%s", out)
	}

	// refs/merge-requests/* is unpushable even by the owner.
	out, code = gitRun(t, dir, aliceEnv, "push", "origin", "HEAD:refs/merge-requests/1/head")
	if code == 0 {
		t.Fatal("client pushed into refs/merge-requests/*")
	}
	if !strings.Contains(out, "server-owned") {
		t.Fatalf("mr-ref denial message:\n%s", out)
	}

	// Unprotect: force-push now goes through.
	_, _, code = inst.ssh(t, aliceKey, "", "repo", "settings", "unprotect", "alice/proj", "main")
	if code != 0 {
		t.Fatal("unprotect failed")
	}
	mustGit(t, dir, aliceEnv, "push", "-q", "--force", "origin", "main")
}
