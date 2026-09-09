package e2e

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The whole flow a user goes through: init on their machine, attach the
// printed key to their repository, start the runner from the config init
// wrote. The runner builds their push and leaves a fork's merge request
// head alone until started with -untrusted.
func TestAttachedRunnerBuildsOwnRepo(t *testing.T) {
	inst := startInstance(t)
	inst.runner = buildRunner(t)
	aliceKey := inst.newKey(t, "alice")
	inst.admin(t, "admin", "user", "create", "alice", "--key", aliceKey+".pub")
	bobKey := inst.newKey(t, "bob")
	inst.admin(t, "admin", "user", "create", "bob", "--key", bobKey+".pub")
	if _, errOut, code := inst.ssh(t, aliceKey, "", "repo", "create", "alice/app"); code != 0 {
		t.Fatalf("repo create: %s", errOut)
	}

	// init on "alice's laptop".
	xdg := t.TempDir()
	initCmd := exec.Command(inst.runner, "init", "-remote", "git@127.0.0.1")
	initCmd.Env = append(os.Environ(), "XDG_CONFIG_HOME="+xdg)
	initOut, err := initCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("init: %v\n%s", err, initOut)
	}
	cdir := filepath.Join(xdg, "gitbay-runner")
	pub, err := os.ReadFile(filepath.Join(cdir, "id_ed25519.pub"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(initOut), strings.TrimSpace(string(pub))) {
		t.Fatalf("init did not print the key:\n%s", initOut)
	}

	// The unattached key claims nothing, even with a build queued.
	work := t.TempDir()
	env := inst.gitEnv(aliceKey)
	mustGit(t, work, env, "clone", inst.sshURL("alice/app"), "w")
	dir := filepath.Join(work, "w")
	os.MkdirAll(filepath.Join(dir, ".gitbay"), 0o755)
	os.WriteFile(filepath.Join(dir, ".gitbay", "ci.yml"), []byte("jobs:\n  unit:\n    steps:\n      - echo built\n"), 0o644)
	mustGit(t, dir, env, "checkout", "-q", "-b", "main")
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, env, "commit", "-q", "-m", "ci")
	mustGit(t, dir, env, "push", "-q", "origin", "main")

	run := func(extra ...string) string {
		t.Helper()
		// No -i in ssh-opts: the identity from the config is what
		// authenticates, which is the point.
		opts := fmt.Sprintf("-p %d -o StrictHostKeyChecking=no -o UserKnownHostsFile=%s -o BatchMode=yes",
			inst.port, filepath.Join(inst.sshDir, "known_hosts"))
		args := append([]string{"-config", filepath.Join(cdir, "config.toml"), "-once",
			"-ssh-opts", opts,
			"-clone-base", fmt.Sprintf("ssh://git@127.0.0.1:%d", inst.port),
			"-workdir", t.TempDir()}, extra...)
		cmd := exec.Command(inst.runner, args...)
		cmd.Env = append(os.Environ(), "XDG_CONFIG_HOME="+xdg, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("runner: %v\n%s", err, out)
		}
		return string(out)
	}
	if out, _, _ := inst.ssh(t, aliceKey, "", "build", "list", "alice/app"); !strings.Contains(out, "unit\tpending") {
		t.Fatalf("build not pending before attach: %s", out)
	}

	// Attach with the printed key.
	if _, errOut, code := inst.ssh(t, aliceKey, string(pub), "repo", "runner", "add", "alice/app"); code != 0 {
		t.Fatalf("repo runner add: %s", errOut)
	}
	out, _, _ := inst.ssh(t, aliceKey, "", "repo", "runner", "list", "alice/app", "--json")
	if !strings.Contains(out, `"username":"alice"`) || strings.Contains(out, `"last_seen":"20`) {
		t.Fatalf("list after attach: %s", out)
	}

	// The runner builds it.
	run()
	if out, _, _ = inst.ssh(t, aliceKey, "", "build", "list", "alice/app"); !strings.Contains(out, "unit\tsuccess") {
		t.Fatalf("build not built by the attached runner: %s", out)
	}
	if out, _, _ = inst.ssh(t, aliceKey, "", "repo", "runner", "list", "alice/app", "--json"); !strings.Contains(out, `"last_seen":"20`) {
		t.Fatalf("no heartbeat after a poll: %s", out)
	}

	// bob forks and opens a merge request: an untrusted build in the target.
	if _, errOut, code := inst.ssh(t, bobKey, "", "repo", "fork", "alice/app"); code != 0 {
		t.Fatalf("fork: %s", errOut)
	}
	bwork := t.TempDir()
	benv := inst.gitEnv(bobKey)
	mustGit(t, bwork, benv, "clone", inst.sshURL("bob/app"), "w")
	bdir := filepath.Join(bwork, "w")
	mustGit(t, bdir, benv, "checkout", "-q", "-b", "feat")
	os.WriteFile(filepath.Join(bdir, "f.txt"), []byte("y\n"), 0o644)
	mustGit(t, bdir, benv, "add", ".")
	mustGit(t, bdir, benv, "commit", "-q", "-m", "change")
	mustGit(t, bdir, benv, "push", "-q", "origin", "feat")
	if _, _, code := inst.ssh(t, bobKey, "", "build", "cancel", "bob/app", "1"); code != 0 {
		t.Fatal("cancel bob's own build")
	}
	if _, errOut, code := inst.ssh(t, bobKey, "", "mr", "create", "alice/app",
		"--source", "bob/app:feat", "--target", "main", "--title", "change"); code != 0 {
		t.Fatalf("mr create: %s", errOut)
	}
	run()
	if out, _, _ = inst.ssh(t, aliceKey, "", "build", "list", "alice/app"); strings.Count(out, "pending") != 1 {
		t.Fatalf("fork head was claimed without -untrusted:\n%s", out)
	}
	run("-untrusted")
	if out, _, _ = inst.ssh(t, aliceKey, "", "build", "list", "alice/app"); strings.Contains(out, "pending") {
		t.Fatalf("fork head not built with -untrusted:\n%s", out)
	}
}
