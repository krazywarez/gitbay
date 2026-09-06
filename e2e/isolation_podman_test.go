package e2e

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// havePodman reports whether a working rootless podman is on this
// machine. The skip is loud on purpose: an isolation test that quietly
// does not run is how isolation regresses (#144).
func havePodman(t *testing.T) bool {
	t.Helper()
	if _, err := exec.LookPath("podman"); err != nil {
		t.Log("SKIPPING ISOLATION TEST: podman is not installed on this machine. " +
			"The container path is NOT covered by this run.")
		return false
	}
	if out, err := exec.Command("podman", "info", "--format", "{{.Host.Security.Rootless}}").CombinedOutput(); err != nil {
		t.Logf("SKIPPING ISOLATION TEST: podman does not work here: %v\n%s", err, out)
		return false
	}
	return true
}

// The fallback that must not exist: with -isolation podman and no podman,
// the runner refuses to start rather than running a build on the host.
// This one needs no podman, so it runs everywhere.
func TestRunnerRefusesToStartWithoutPodman(t *testing.T) {
	bin := buildRunner(t)
	cmd := exec.Command(bin, "-once", "-remote", "git@127.0.0.1",
		"-isolation", "podman", "-workdir", t.TempDir())
	// An empty PATH is the reliable way to make podman missing whether or
	// not this machine has one.
	cmd.Env = []string{"PATH=" + t.TempDir(), "HOME=" + t.TempDir()}
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("the runner started without podman:\n%s", out)
	}
	if !strings.Contains(string(out), "podman") {
		t.Errorf("refusal does not say podman is the problem:\n%s", out)
	}
	if !strings.Contains(string(out), "runner-podman-setup.sh") {
		t.Errorf("refusal does not say how to fix it:\n%s", out)
	}
}

// An unknown mode is refused rather than guessed at.
func TestRunnerRefusesUnknownIsolation(t *testing.T) {
	bin := buildRunner(t)
	cmd := exec.Command(bin, "-once", "-remote", "git@127.0.0.1",
		"-isolation", "chroot", "-workdir", t.TempDir())
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("an unknown isolation mode started:\n%s", out)
	}
	if !strings.Contains(string(out), "podman or none") {
		t.Errorf("refusal does not name the valid modes:\n%s", out)
	}
}

// With podman, a step runs in a container: it cannot read the runner's
// SSH key, and it does not see the runner's home.
func TestPodmanStepCannotReachTheRunnersKey(t *testing.T) {
	if !havePodman(t) {
		t.Skip("no podman")
	}
	inst := startInstance(t)
	inst.runner = buildRunner(t)
	aliceKey := inst.newKey(t, "alice")
	inst.admin(t, "admin", "user", "create", "alice", "--key", aliceKey+".pub")
	runnerKey := inst.newKey(t, "ci")
	inst.admin(t, "admin", "user", "create", "ci", "--key", runnerKey+".pub", "--admin")
	inst.ssh(t, aliceKey, "", "repo", "create", "alice/app")

	env := inst.gitEnv(aliceKey)
	work := t.TempDir()
	mustGit(t, work, env, "clone", inst.sshURL("alice/app"), "w")
	dir := filepath.Join(work, "w")
	os.MkdirAll(filepath.Join(dir, ".gitbay"), 0o755)
	// The step tries to read the key the runner authenticates with, and
	// to list the runner's home. Both must fail inside the container.
	os.WriteFile(filepath.Join(dir, ".gitbay", "ci.yml"), []byte(
		"jobs:\n  peek:\n    image: docker.io/library/debian:stable-slim\n    steps:\n"+
			"      - 'if cat "+runnerKey+" 2>/dev/null; then echo LEAKED-KEY; exit 1; fi; echo no-key'\n"+
			"      - 'echo HOME=$HOME; ls /workspace'\n"), 0o644)
	mustGit(t, dir, env, "checkout", "-q", "-b", "main")
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, env, "commit", "-q", "-m", "base")
	mustGit(t, dir, env, "push", "-q", "origin", "main")

	runnerPodmanOnce(t, inst, runnerKey)
	out, _, _ := inst.ssh(t, aliceKey, "", "build", "list", "alice/app")
	if !strings.Contains(out, "success") {
		t.Fatalf("the containerised build did not pass:\n%s", out)
	}
	log, _, _ := inst.ssh(t, aliceKey, "", "build", "log", "alice/app", "1")
	if strings.Contains(log, "LEAKED-KEY") {
		t.Errorf("a step read the runner's ssh key:\n%s", log)
	}
	if !strings.Contains(log, "no-key") {
		t.Errorf("the step did not run as expected:\n%s", log)
	}
}

// A pull failure fails the build and says why, rather than retrying or
// silently choosing another image.
func TestPodmanPullFailureFailsTheBuild(t *testing.T) {
	if !havePodman(t) {
		t.Skip("no podman")
	}
	inst := startInstance(t)
	inst.runner = buildRunner(t)
	aliceKey := inst.newKey(t, "alice")
	inst.admin(t, "admin", "user", "create", "alice", "--key", aliceKey+".pub")
	runnerKey := inst.newKey(t, "ci")
	inst.admin(t, "admin", "user", "create", "ci", "--key", runnerKey+".pub", "--admin")
	inst.ssh(t, aliceKey, "", "repo", "create", "alice/app")

	env := inst.gitEnv(aliceKey)
	work := t.TempDir()
	mustGit(t, work, env, "clone", inst.sshURL("alice/app"), "w")
	dir := filepath.Join(work, "w")
	os.MkdirAll(filepath.Join(dir, ".gitbay"), 0o755)
	os.WriteFile(filepath.Join(dir, ".gitbay", "ci.yml"), []byte(
		"jobs:\n  nope:\n    image: localhost/gitbay-no-such-image:v0\n    steps:\n      - echo unreachable\n"), 0o644)
	mustGit(t, dir, env, "checkout", "-q", "-b", "main")
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, env, "commit", "-q", "-m", "base")
	mustGit(t, dir, env, "push", "-q", "origin", "main")

	runnerPodmanOnce(t, inst, runnerKey)
	out, _, _ := inst.ssh(t, aliceKey, "", "build", "list", "alice/app")
	if !strings.Contains(out, "failure") {
		t.Fatalf("a build with an unpullable image did not fail:\n%s", out)
	}
	log, _, _ := inst.ssh(t, aliceKey, "", "build", "log", "alice/app", "1")
	if !strings.Contains(log, "gitbay-no-such-image") {
		t.Errorf("the log does not name the image that could not be pulled:\n%s", log)
	}
	if strings.Contains(log, "unreachable") {
		t.Error("a step ran despite the image failing to start")
	}
}

func runnerPodmanOnce(t *testing.T, inst *instance, key string) {
	t.Helper()
	opts := fmt.Sprintf("-p %d -i %s -o IdentitiesOnly=yes -o StrictHostKeyChecking=no -o UserKnownHostsFile=%s -o BatchMode=yes",
		inst.port, key, filepath.Join(inst.sshDir, "known_hosts"))
	cmd := exec.Command(inst.runner, "-once",
		"-remote", "git@127.0.0.1",
		"-ssh-opts", opts,
		"-isolation", "podman",
		"-clone-base", fmt.Sprintf("ssh://git@127.0.0.1:%d", inst.port),
		"-workdir", t.TempDir())
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("runner: %v\n%s", err, out)
	}
}
