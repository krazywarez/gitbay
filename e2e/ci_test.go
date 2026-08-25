package e2e

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func buildRunner(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "gitbay-runner")
	cmd := exec.Command("go", "build", "-o", bin, "gitbay.org/gitbay/cmd/gitbay-runner")
	cmd.Dir = ".."
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build gitbay-runner: %v\n%s", err, out)
	}
	return bin
}

// runnerOnce processes at most one pending build with the given key.
func (i *instance) runnerOnce(t *testing.T, key string) string {
	t.Helper()
	opts := fmt.Sprintf("-p %d -i %s -o IdentitiesOnly=yes -o StrictHostKeyChecking=no -o UserKnownHostsFile=%s -o BatchMode=yes",
		i.port, key, filepath.Join(i.sshDir, "known_hosts"))
	cmd := exec.Command(i.runner, "-once",
		"-remote", "git@127.0.0.1",
		"-ssh-opts", opts,
		"-clone-base", fmt.Sprintf("ssh://git@127.0.0.1:%d", i.port),
		"-workdir", t.TempDir())
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("runner: %v\n%s", err, out)
	}
	return string(out)
}

func TestCI(t *testing.T) {
	inst := startInstance(t)
	inst.runner = buildRunner(t)
	aliceKey := inst.newKey(t, "alice")
	inst.admin(t, "admin", "user", "create", "alice", "--key", aliceKey+".pub")
	runnerKey := inst.newKey(t, "ci")
	inst.admin(t, "admin", "user", "create", "ci", "--key", runnerKey+".pub", "--admin")

	if _, errOut, code := inst.ssh(t, aliceKey, "", "repo", "create", "alice/app"); code != 0 {
		t.Fatalf("repo create: %s", errOut)
	}
	work := t.TempDir()
	env := inst.gitEnv(aliceKey)
	mustGit(t, work, env, "clone", inst.sshURL("alice/app"), "w")
	dir := filepath.Join(work, "w")
	os.MkdirAll(filepath.Join(dir, ".gitbay"), 0o755)
	os.WriteFile(filepath.Join(dir, ".gitbay", "ci.yml"), []byte(
		"jobs:\n  ok:\n    steps:\n      - echo hello from $GITBAY_JOB\n  broken:\n    steps:\n      - \"false\"\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "f.txt"), []byte("x\n"), 0o644)
	mustGit(t, dir, env, "checkout", "-q", "-b", "main")
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, env, "commit", "-q", "-m", "base")
	mustGit(t, dir, env, "push", "-q", "origin", "main")
	sha := strings.TrimSpace(mustGit(t, dir, env, "rev-parse", "HEAD"))

	// The push queued one pending build per job, with pending statuses.
	out, _, _ := inst.ssh(t, aliceKey, "", "build", "list", "alice/app")
	if !strings.Contains(out, "broken\tpending") || !strings.Contains(out, "ok\tpending") {
		t.Fatalf("builds not queued:\n%s", out)
	}
	out, _, _ = inst.ssh(t, aliceKey, "", "status", "list", "alice/app", sha)
	if !strings.Contains(out, "ci/ok") || !strings.Contains(out, "pending") {
		t.Fatalf("pending statuses missing:\n%s", out)
	}

	// Non-admins cannot claim jobs.
	if _, _, code := inst.ssh(t, aliceKey, "", "runner", "next"); code != 4 {
		t.Fatalf("non-admin claimed a build: exit %d", code)
	}

	// The runner processes both jobs ("broken" sorts first).
	inst.runnerOnce(t, runnerKey)
	inst.runnerOnce(t, runnerKey)

	out, _, _ = inst.ssh(t, aliceKey, "", "build", "list", "alice/app")
	if !strings.Contains(out, "ok\tsuccess") || !strings.Contains(out, "broken\tfailure") {
		t.Fatalf("build outcomes wrong:\n%s", out)
	}
	// Logs captured the step output and the failure.
	var okN, brokenN string
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		f := strings.Split(l, "\t")
		if f[1] == "ok" {
			okN = f[0]
		} else {
			brokenN = f[0]
		}
	}
	out, _, _ = inst.ssh(t, aliceKey, "", "build", "log", "alice/app", okN)
	if !strings.Contains(out, "hello from ok") {
		t.Fatalf("ok log:\n%s", out)
	}
	out, _, _ = inst.ssh(t, aliceKey, "", "build", "log", "alice/app", brokenN)
	if !strings.Contains(out, "step failed") {
		t.Fatalf("broken log:\n%s", out)
	}
	// Statuses resolved, with target URLs pointing at the build pages.
	out, _, _ = inst.ssh(t, aliceKey, "", "status", "list", "alice/app", sha, "--json")
	if !strings.Contains(out, `"ci/ok","state":"success"`) && !strings.Contains(out, `"state":"success"`) {
		t.Fatalf("status not success:\n%s", out)
	}
	if !strings.Contains(out, "/alice/app/builds/") {
		t.Fatalf("status target url missing:\n%s", out)
	}

	// Web: list page and log page.
	status, body := inst.get(t, "/alice/app/builds")
	if status != 200 || !strings.Contains(body, "ok") || !strings.Contains(body, "failure") {
		t.Fatalf("builds page: %d\n%s", status, body)
	}
	if _, body = inst.get(t, "/alice/app/builds/"+okN); !strings.Contains(body, "hello from ok") {
		t.Fatalf("build log page:\n%s", body)
	}

	// --- secrets: stdin in, names-only out, injected into the build env ---
	if _, errOut, code := inst.ssh(t, aliceKey, "hunter2\n", "repo", "secret", "set", "alice/app", "MY_TOKEN"); code != 0 {
		t.Fatalf("secret set: %s", errOut)
	}
	if _, _, code := inst.ssh(t, aliceKey, "x\n", "repo", "secret", "set", "alice/app", "bad-name"); code != 2 {
		t.Fatal("bad secret name accepted")
	}
	out, _, _ = inst.ssh(t, aliceKey, "", "repo", "secret", "list", "alice/app")
	if !strings.Contains(out, "MY_TOKEN") || strings.Contains(out, "hunter2") {
		t.Fatalf("secret list leaked or missed: %s", out)
	}

	// --- schedules and manual trigger ---
	os.WriteFile(filepath.Join(dir, ".gitbay", "ci.yml"), []byte(
		"jobs:\n  usesecret:\n    steps:\n      - echo token=$MY_TOKEN\n  nightly:\n    schedule: \"0 6 * * 1\"\n    steps:\n      - echo scheduled ran\n"), 0o644)
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, env, "commit", "-q", "-m", "secrets and schedule")
	mustGit(t, dir, env, "push", "-q", "origin", "main")

	// The push queued only the unscheduled job.
	out, _, _ = inst.ssh(t, aliceKey, "", "build", "list", "alice/app")
	if !strings.Contains(out, "usesecret\tpending") || strings.Contains(out, "nightly") {
		t.Fatalf("scheduled job queued on push:\n%s", out)
	}
	inst.runnerOnce(t, runnerKey)
	out, _, _ = inst.ssh(t, aliceKey, "", "build", "list", "alice/app")
	usecretN := strings.Split(out, "\t")[0]
	out, _, _ = inst.ssh(t, aliceKey, "", "build", "log", "alice/app", usecretN)
	if !strings.Contains(out, "token=hunter2") {
		t.Fatalf("secret not injected:\n%s", out)
	}
	// The scheduled job runs on demand via trigger.
	if _, errOut, code := inst.ssh(t, aliceKey, "", "build", "trigger", "alice/app", "nightly"); code != 0 {
		t.Fatalf("trigger: %s", errOut)
	}
	if _, _, code := inst.ssh(t, aliceKey, "", "build", "trigger", "alice/app", "nosuch"); code != 3 {
		t.Fatal("triggered a job that does not exist")
	}
	inst.runnerOnce(t, runnerKey)
	out, _, _ = inst.ssh(t, aliceKey, "", "build", "list", "alice/app")
	if !strings.Contains(out, "nightly\tsuccess") {
		t.Fatalf("triggered build did not run:\n%s", out)
	}
	// Removing the secret stops injection.
	if _, _, code := inst.ssh(t, aliceKey, "", "repo", "secret", "remove", "alice/app", "MY_TOKEN"); code != 0 {
		t.Fatal("secret remove failed")
	}

	// --- tag-triggered jobs ---
	os.WriteFile(filepath.Join(dir, ".gitbay", "ci.yml"), []byte(
		"jobs:\n  test:\n    steps:\n      - echo branch build\n  publish:\n    tags: \"v*\"\n    steps:\n      - echo publishing $GITBAY_REF\n"), 0o644)
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, env, "commit", "-q", "-m", "tag job")
	mustGit(t, dir, env, "push", "-q", "origin", "main")
	// The branch push queued only the branch job.
	out, _, _ = inst.ssh(t, aliceKey, "", "build", "list", "alice/app")
	if strings.Contains(out, "publish") {
		t.Fatalf("tag job queued on branch push:\n%s", out)
	}
	// An annotated tag queues the tag job, with the peeled commit as sha.
	mustGit(t, dir, env, "tag", "-a", "-m", "rel", "v1.0.0")
	mustGit(t, dir, env, "push", "-q", "origin", "v1.0.0")
	headSHA := strings.TrimSpace(mustGit(t, dir, env, "rev-parse", "HEAD"))
	out, _, _ = inst.ssh(t, aliceKey, "", "build", "list", "alice/app")
	if !strings.Contains(out, "publish\tpending\t"+headSHA[:10]) || !strings.Contains(out, "v1.0.0") {
		t.Fatalf("tag build missing or unpeeled:\n%s", out)
	}
	// A non-matching tag queues nothing.
	mustGit(t, dir, env, "tag", "nightly-1")
	mustGit(t, dir, env, "push", "-q", "origin", "nightly-1")
	out2, _, _ := inst.ssh(t, aliceKey, "", "build", "list", "alice/app")
	if strings.Count(out2, "publish") != strings.Count(out, "publish") {
		t.Fatalf("non-matching tag queued a build:\n%s", out2)
	}
	inst.runnerOnce(t, runnerKey) // branch "test" job
	inst.runnerOnce(t, runnerKey) // tag "publish" job
	out, _, _ = inst.ssh(t, aliceKey, "", "build", "list", "alice/app")
	if !strings.Contains(out, "publish\tsuccess") {
		t.Fatalf("tag build did not run:\n%s", out)
	}
	// schedule and tags together are refused.
	os.WriteFile(filepath.Join(dir, ".gitbay", "ci.yml"), []byte(
		"jobs:\n  both:\n    schedule: \"0 6 * * *\"\n    tags: \"v*\"\n    steps: [echo x]\n"), 0o644)
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, env, "commit", "-q", "-m", "both triggers")
	mustGit(t, dir, env, "push", "-q", "origin", "main")
	shaBoth := strings.TrimSpace(mustGit(t, dir, env, "rev-parse", "HEAD"))
	out, _, _ = inst.ssh(t, aliceKey, "", "status", "list", "alice/app", shaBoth)
	if !strings.Contains(out, "ci/config") || !strings.Contains(out, "failure") {
		t.Fatalf("mutually exclusive triggers not refused:\n%s", out)
	}

	// A broken ci.yml surfaces as a failed ci/config status.
	os.WriteFile(filepath.Join(dir, ".gitbay", "ci.yml"), []byte("jobs: {bad name: {steps: [x]}}\n"), 0o644)
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, env, "commit", "-q", "-m", "break config")
	mustGit(t, dir, env, "push", "-q", "origin", "main")
	sha2 := strings.TrimSpace(mustGit(t, dir, env, "rev-parse", "HEAD"))
	out, _, _ = inst.ssh(t, aliceKey, "", "status", "list", "alice/app", sha2)
	if !strings.Contains(out, "ci/config") || !strings.Contains(out, "failure") {
		t.Fatalf("config failure status missing:\n%s", out)
	}
}
