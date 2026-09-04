package e2e

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

// A failed build mails the repo owner with the log tail; green builds
// stay silent.
func TestBuildFailureMail(t *testing.T) {
	smtp := startFakeSMTP(t)
	inst := startInstanceWith(t, fmt.Sprintf(
		"[mail]\nsmtp_host = %q\nfrom = \"noreply@gitbay.test\"\n", smtp.addr))
	inst.runner = buildRunner(t)
	aliceKey := inst.newKey(t, "alice")
	inst.admin(t, "admin", "user", "create", "alice", "--key", aliceKey+".pub",
		"--email", "alice@example.test", "--verified")
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
		"jobs:\n  ok:\n    steps:\n      - echo fine\n  broken:\n    steps:\n      - echo the dataset went stale && false\n"), 0o644)
	mustGit(t, dir, env, "checkout", "-q", "-b", "main")
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, env, "commit", "-q", "-m", "base")
	mustGit(t, dir, env, "push", "-q", "origin", "main")

	inst.runnerOnce(t, runnerKey) // broken (sorts first)
	inst.runnerOnce(t, runnerKey) // ok
	mail := smtp.waitFor(t, "alice@example.test", "failed")
	if !strings.Contains(mail, "broken") || !strings.Contains(mail, "the dataset went stale") ||
		!strings.Contains(mail, "/alice/app/builds/") {
		t.Fatalf("failure mail missing detail:\n%s", mail)
	}
	// Only the failure mailed: no message mentions the green job.
	for _, m := range smtp.mailTo("alice@example.test") {
		if strings.Contains(m, "build") && strings.Contains(m, " ok ") && strings.Contains(m, "failed") == false {
			t.Fatalf("green build mailed:\n%s", m)
		}
	}
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

// runnerJobs runs a runner with -jobs n until it has nothing left to do,
// and returns its output. Unlike runnerOnce it is not bounded to one
// build, so it is stopped when the queue drains.
func (i *instance) runnerJobs(t *testing.T, key, repo string, jobs int) string {
	t.Helper()
	opts := fmt.Sprintf("-p %d -i %s -o IdentitiesOnly=yes -o StrictHostKeyChecking=no -o UserKnownHostsFile=%s -o BatchMode=yes",
		i.port, key, filepath.Join(i.sshDir, "known_hosts"))
	cmd := exec.Command(i.runner,
		"-jobs", fmt.Sprint(jobs),
		"-poll", "200ms",
		"-remote", "git@127.0.0.1",
		"-ssh-opts", opts,
		"-clone-base", fmt.Sprintf("ssh://git@127.0.0.1:%d", i.port),
		"-workdir", t.TempDir())
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { cmd.Process.Kill(); cmd.Wait() }()

	// Wait for every queued build to leave the pending and running states.
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		out, _, code := i.ssh(t, key, "", "build", "list", repo, "--json")
		if code == 0 && !strings.Contains(out, `"status":"pending"`) &&
			!strings.Contains(out, `"status":"running"`) {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	return buf.String()
}

// -jobs N runs N builds at once. ClaimBuild has always been a single
// transaction that selects and updates, so several workers claiming
// together is safe; the runner simply never used more than one (#115).
func TestRunnerConcurrentJobs(t *testing.T) {
	inst := startInstance(t)
	inst.runner = buildRunner(t)
	aliceKey := inst.newKey(t, "alice")
	inst.admin(t, "admin", "user", "create", "alice", "--key", aliceKey+".pub", "--admin")
	if _, errOut, code := inst.ssh(t, aliceKey, "", "repo", "create", "alice/app"); code != 0 {
		t.Fatalf("repo create: %s", errOut)
	}

	// Four jobs, each sleeping longer than the poll interval, so serial
	// execution and concurrent execution are distinguishable.
	ci := "jobs:\n"
	for _, name := range []string{"one", "two", "three", "four"} {
		ci += fmt.Sprintf("  %s:\n    steps:\n      - sleep 1\n      - echo done-%s\n", name, name)
	}
	env := inst.gitEnv(aliceKey)
	work := t.TempDir()
	mustGit(t, work, env, "clone", inst.sshURL("alice/app"), "w")
	dir := filepath.Join(work, "w")
	os.MkdirAll(filepath.Join(dir, ".gitbay"), 0o755)
	os.WriteFile(filepath.Join(dir, ".gitbay", "ci.yml"), []byte(ci), 0o644)
	mustGit(t, dir, env, "checkout", "-q", "-b", "main")
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, env, "commit", "-q", "-m", "ci")
	mustGit(t, dir, env, "push", "-q", "origin", "main")

	out := inst.runnerJobs(t, aliceKey, "alice/app", 4)

	listing, _, code := inst.ssh(t, aliceKey, "", "build", "list", "alice/app", "--json")
	if code != 0 {
		t.Fatalf("build list failed:\n%s", out)
	}
	for _, name := range []string{"one", "two", "three", "four"} {
		if !strings.Contains(listing, `"job":"`+name+`"`) {
			t.Fatalf("%s never ran:\n%s\n%s", name, listing, out)
		}
	}
	if strings.Contains(listing, `"status":"pending"`) || strings.Contains(listing, `"status":"running"`) {
		t.Fatalf("builds did not finish:\n%s", listing)
	}
	// Overlap, asserted from the runner's own log rather than from wall
	// clock: elapsed time also covers four concurrent clones and the
	// polling this test does, and would make a slow machine look serial.
	// Every build announces itself when it starts and again when it
	// finishes, so concurrency is "a build started before the first one
	// finished".
	started, firstFinish := 0, -1
	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.Contains(line, "alice/app"):
			started++
		case strings.Contains(line, ": success"), strings.Contains(line, ": failure"):
			if firstFinish < 0 {
				firstFinish = started
			}
		}
	}
	if started != 4 {
		t.Fatalf("%d builds started, want 4:\n%s", started, out)
	}
	if firstFinish < 2 {
		t.Errorf("only %d build(s) had started when the first finished; -jobs 4 ran them serially\n%s",
			firstFinish, out)
	}
}
