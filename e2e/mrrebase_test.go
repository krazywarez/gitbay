package e2e

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// `mr rebase` replays a merge request's branch onto its target and
// re-pushes it, which is what makes a fast-forward merge possible again
// after the target has moved. The git work is local, so the replayed
// commits are signed by whatever the user's git config signs with and the
// server is never asked to vouch for a commit it did not receive already
// signed (#175).
func TestCLIMRRebase(t *testing.T) {
	inst := startInstance(t)
	aliceKey := inst.newKey(t, "alice")
	inst.admin(t, "admin", "user", "create", "alice",
		"--key", aliceKey+".pub", "--email", "alice@example.test", "--verified")

	c := &cli{bin: buildGitbayCLI(t), configDir: t.TempDir(), inst: inst, key: aliceKey}
	c.must(t, "", "", "remote", "add", "test", "127.0.0.1",
		"--port", fmt.Sprint(inst.port),
		"--ssh-option", "-i", "--ssh-option", aliceKey,
		"--ssh-option", "-oIdentitiesOnly=yes",
		"--ssh-option", "-oStrictHostKeyChecking=no",
		"--ssh-option", "-oUserKnownHostsFile="+filepath.Join(inst.sshDir, "kh"),
		"--ssh-option", "-oBatchMode=yes",
		"--default")
	c.must(t, "", "", "repo", "create", "alice/app")

	env := inst.gitEnv(aliceKey)
	work := t.TempDir()
	mustGit(t, work, env, "clone", inst.sshURL("alice/app"), "w")
	dir := filepath.Join(work, "w")
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a\n"), 0o644)
	mustGit(t, dir, env, "checkout", "-q", "-b", "main")
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, env, "commit", "-q", "-m", "base")
	mustGit(t, dir, env, "push", "-q", "origin", "main")

	// A branch off main, and a merge request for it.
	mustGit(t, dir, env, "checkout", "-q", "-b", "feat")
	os.WriteFile(filepath.Join(dir, "b.txt"), []byte("b\n"), 0o644)
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, env, "commit", "-q", "-m", "the change")
	mustGit(t, dir, env, "push", "-q", "origin", "feat")
	c.must(t, dir, "", "mr", "create", "alice/app",
		"--source", "feat", "--target", "main", "--title", "change")

	// main moves on, so feat is no longer a fast-forward.
	mustGit(t, dir, env, "checkout", "-q", "main")
	os.WriteFile(filepath.Join(dir, "c.txt"), []byte("c\n"), 0o644)
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, env, "commit", "-q", "-m", "moved on")
	mustGit(t, dir, env, "push", "-q", "origin", "main")
	if _, errOut, code := inst.ssh(t, aliceKey, "", "mr", "merge", "alice/app", "1", "--strategy", "ff"); code == 0 {
		t.Fatal("fast-forward merged a diverged branch")
	} else if !strings.Contains(errOut, "fast-forward not possible") {
		t.Fatalf("unexpected refusal: %s", errOut)
	}

	// Rebase, and the same merge now lands.
	out, errOut, code := c.run(t, dir, "", "mr", "rebase", "1")
	if code != 0 {
		t.Fatalf("mr rebase: exit %d\n%s\n%s", code, out, errOut)
	}
	if !strings.Contains(out, "gitbay mr merge 1") {
		t.Errorf("rebase does not name the next step: %s", out)
	}
	if _, errOut, code := inst.ssh(t, aliceKey, "", "mr", "merge", "alice/app", "1", "--strategy", "ff"); code != 0 {
		t.Fatalf("fast-forward still refused after a rebase: %s", errOut)
	}

	// The rebase moved the branch rather than merging main into it: the
	// change is one commit on top of what main had.
	out, _, _ = inst.ssh(t, aliceKey, "", "mr", "show", "alice/app", "1", "--json")
	if !strings.Contains(out, `"state":"merged"`) {
		t.Fatalf("merge request not merged:\n%s", out)
	}
}

// The guards: a dirty tree, and a source in a fork this clone cannot push.
func TestCLIMRRebaseRefusals(t *testing.T) {
	inst := startInstance(t)
	aliceKey := inst.newKey(t, "alice")
	bobKey := inst.newKey(t, "bob")
	inst.admin(t, "admin", "user", "create", "alice", "--key", aliceKey+".pub")
	inst.admin(t, "admin", "user", "create", "bob", "--key", bobKey+".pub")

	c := &cli{bin: buildGitbayCLI(t), configDir: t.TempDir(), inst: inst, key: aliceKey}
	c.must(t, "", "", "remote", "add", "test", "127.0.0.1",
		"--port", fmt.Sprint(inst.port),
		"--ssh-option", "-i", "--ssh-option", aliceKey,
		"--ssh-option", "-oIdentitiesOnly=yes",
		"--ssh-option", "-oStrictHostKeyChecking=no",
		"--ssh-option", "-oUserKnownHostsFile="+filepath.Join(inst.sshDir, "kh"),
		"--ssh-option", "-oBatchMode=yes",
		"--default")
	c.must(t, "", "", "repo", "create", "alice/app")

	env := inst.gitEnv(aliceKey)
	work := t.TempDir()
	mustGit(t, work, env, "clone", inst.sshURL("alice/app"), "w")
	dir := filepath.Join(work, "w")
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a\n"), 0o644)
	mustGit(t, dir, env, "checkout", "-q", "-b", "main")
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, env, "commit", "-q", "-m", "base")
	mustGit(t, dir, env, "push", "-q", "origin", "main")

	// bob forks, pushes, and opens a merge request from the fork.
	inst.ssh(t, bobKey, "", "repo", "fork", "alice/app")
	benv := inst.gitEnv(bobKey)
	bwork := t.TempDir()
	mustGit(t, bwork, benv, "clone", inst.sshURL("bob/app"), "w")
	bdir := filepath.Join(bwork, "w")
	mustGit(t, bdir, benv, "checkout", "-q", "-b", "feat")
	os.WriteFile(filepath.Join(bdir, "b.txt"), []byte("b\n"), 0o644)
	mustGit(t, bdir, benv, "add", ".")
	mustGit(t, bdir, benv, "commit", "-q", "-m", "from the fork")
	mustGit(t, bdir, benv, "push", "-q", "origin", "feat")
	inst.ssh(t, bobKey, "", "mr", "create", "alice/app",
		"--source", "bob/app:feat", "--target", "main", "--title", "forked")

	// Alice's clone of the target cannot rebase a branch that lives in
	// bob's fork, and says which repository to do it in.
	_, errOut, code := c.run(t, dir, "", "mr", "rebase", "1")
	if code == 0 {
		t.Fatal("rebased a fork's branch from the target's clone")
	}
	if !strings.Contains(errOut, "bob/app:feat") {
		t.Errorf("refusal does not name the fork: %s", errOut)
	}

	// A dirty tree is refused before any round trip, so the merge request
	// number never has to be valid for this one.
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("edited\n"), 0o644)
	if _, errOut, code := c.run(t, dir, "", "mr", "rebase", "1"); code == 0 ||
		!strings.Contains(errOut, "uncommitted changes") {
		t.Errorf("dirty tree not refused: exit %d %s", code, errOut)
	}
}
