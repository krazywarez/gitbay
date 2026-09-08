package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"
)

// The runner drains on SIGTERM: the build in flight runs to its end and
// is reported, and nothing more is claimed (#179). The drain shipped
// broken once because only the process was tested and never the unit:
// systemd's default KillMode signals every process in the cgroup, the
// step and the log session included, so the runner drained a build
// whose steps were already dead. deploy/gitbay-runner.override.conf
// sets KillMode=mixed for that reason, and the systemd test below runs
// the runner as a real transient unit under both modes (#184).

// stopFixture starts an instance with one repository whose single job
// sleeps long enough to be stopped mid-step, and returns the admin key
// that both pushes and runs the runner.
func stopFixture(t *testing.T) (*instance, string) {
	t.Helper()
	inst := startInstance(t)
	inst.runner = buildRunner(t)
	key := inst.newKey(t, "alice")
	inst.admin(t, "admin", "user", "create", "alice", "--key", key+".pub", "--admin")
	if _, errOut, code := inst.ssh(t, key, "", "repo", "create", "alice/app"); code != 0 {
		t.Fatalf("repo create: %s", errOut)
	}
	work := t.TempDir()
	env := inst.gitEnv(key)
	mustGit(t, work, env, "clone", inst.sshURL("alice/app"), "w")
	dir := filepath.Join(work, "w")
	os.MkdirAll(filepath.Join(dir, ".gitbay"), 0o755)
	os.WriteFile(filepath.Join(dir, ".gitbay", "ci.yml"),
		[]byte("jobs:\n  slow:\n    steps:\n      - sleep 4; echo drained\n"), 0o644)
	mustGit(t, dir, env, "checkout", "-q", "-b", "main")
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, env, "commit", "-q", "-m", "ci")
	mustGit(t, dir, env, "push", "-q", "origin", "main")
	return inst, key
}

// runnerArgs is the command line the runner tests use, minus the binary.
func (i *instance) runnerArgs(t *testing.T, key string) []string {
	opts := fmt.Sprintf("-p %d -i %s -o IdentitiesOnly=yes -o StrictHostKeyChecking=no -o UserKnownHostsFile=%s -o BatchMode=yes",
		i.port, key, filepath.Join(i.sshDir, "known_hosts"))
	return []string{
		"-poll", "200ms",
		"-isolation", "none",
		"-remote", "git@127.0.0.1",
		"-ssh-opts", opts,
		"-clone-base", fmt.Sprintf("ssh://git@127.0.0.1:%d", i.port),
		"-workdir", t.TempDir(),
	}
}

func (i *instance) buildStatus(t *testing.T, key string) string {
	t.Helper()
	out, _, _ := i.ssh(t, key, "", "build", "list", "alice/app", "--json")
	var env struct {
		Data []struct {
			Status string `json:"status"`
		} `json:"data"`
	}
	json.Unmarshal([]byte(out), &env)
	if len(env.Data) == 0 {
		return ""
	}
	return env.Data[0].Status
}

func TestRunnerDrainsOnSIGTERM(t *testing.T) {
	inst, key := stopFixture(t)
	cmd := exec.Command(inst.runner, inst.runnerArgs(t, key)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { cmd.Process.Kill(); cmd.Wait() }()

	waitFor(t, "the build to be claimed", func() bool { return inst.buildStatus(t, key) == "running" })
	// The step is asleep. Signal the runner alone, as KillMode=mixed does.
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	select {
	case err := <-exited:
		if err != nil {
			t.Fatalf("runner exited %v after SIGTERM; output:\n%s", err, buf.String())
		}
	case <-time.After(30 * time.Second):
		t.Fatalf("runner did not exit within 30s of SIGTERM; output:\n%s", buf.String())
	}
	if !strings.Contains(buf.String(), "draining") {
		t.Fatalf("runner did not announce the drain:\n%s", buf.String())
	}
	if st := inst.buildStatus(t, key); st != "success" {
		t.Fatalf("build after drain is %q, want success; runner output:\n%s", st, buf.String())
	}
	if out, _, _ := inst.ssh(t, key, "", "build", "log", "alice/app", "1"); !strings.Contains(out, "drained") {
		t.Fatalf("step did not run to its end:\n%s", out)
	}
}

// The drop-in the runner runs under. Read here so a change to it fails a
// test rather than the next production stop.
func runnerDropIn(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "deploy", "gitbay-runner.override.conf"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func dropInValue(conf, key string) string {
	m := regexp.MustCompile(`(?m)^`+key+`=(.*)$`).FindAllStringSubmatch(conf, -1)
	if len(m) == 0 {
		return ""
	}
	return strings.TrimSpace(m[len(m)-1][1]) // the last assignment wins, as in systemd
}

// The unit must let the drain happen: the stop signal reaches the runner
// alone, and the stop timeout outlasts a build plus the report retries.
func TestRunnerDropInLetsTheDrainHappen(t *testing.T) {
	conf := runnerDropIn(t)
	if mode := dropInValue(conf, "KillMode"); mode != "mixed" {
		t.Fatalf("KillMode=%q, want mixed: control-group signals the step and the log session with the runner", mode)
	}
	stop, err := time.ParseDuration(strings.ReplaceAll(dropInValue(conf, "TimeoutStopSec"), "min", "m"))
	if err != nil {
		t.Fatalf("TimeoutStopSec: %v", err)
	}
	m := regexp.MustCompile(`-timeout (\S+)`).FindStringSubmatch(dropInValue(conf, "ExecStart"))
	if m == nil {
		t.Fatal("ExecStart has no -timeout")
	}
	build, err := time.ParseDuration(m[1])
	if err != nil {
		t.Fatalf("-timeout: %v", err)
	}
	// Half a minute of report retries after the build's own limit (#179).
	if stop < build+30*time.Second {
		t.Fatalf("TimeoutStopSec %v cannot outlast a %v build and its report retries", stop, build)
	}
}

// haveUserSystemd reports whether transient user units can be started
// here: a Linux host with a systemd user manager for this account.
func haveUserSystemd(t *testing.T) bool {
	t.Helper()
	if _, err := exec.LookPath("systemd-run"); err != nil {
		return false
	}
	return exec.Command("systemd-run", "--user", "--quiet", "--wait", "--collect", "true").Run() == nil
}

// The runner as a transient unit, stopped by systemd. Under the drop-in's
// KillMode the build in flight is reported a success; under systemd's
// default it is not, which is the failure that shipped once.
func TestRunnerDrainUnderSystemd(t *testing.T) {
	if !haveUserSystemd(t) {
		t.Skip("no systemd user manager")
	}
	mode := dropInValue(runnerDropIn(t), "KillMode")
	for _, tc := range []struct {
		mode    string
		drained bool
	}{
		{mode, true},
		{"control-group", false},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			inst, key := stopFixture(t)
			unit := fmt.Sprintf("gitbay-runner-test-%d-%s", os.Getpid(), tc.mode)
			args := append([]string{"--user", "--quiet", "--collect", "--unit", unit,
				"-p", "KillMode=" + tc.mode, "-p", "TimeoutStopSec=60",
				"--setenv=GIT_CONFIG_NOSYSTEM=1", "--setenv=GIT_CONFIG_GLOBAL=/dev/null",
				inst.runner}, inst.runnerArgs(t, key)...)
			if out, err := exec.Command("systemd-run", args...).CombinedOutput(); err != nil {
				t.Fatalf("systemd-run: %v\n%s", err, out)
			}
			defer exec.Command("systemctl", "--user", "kill", "-s", "SIGKILL", unit).Run()

			waitFor(t, "the build to be claimed", func() bool { return inst.buildStatus(t, key) == "running" })
			// systemctl stop returns when the unit is down: after the
			// drain, or after the cgroup was signalled and emptied.
			stop := exec.Command("systemctl", "--user", "stop", unit)
			if out, err := stop.CombinedOutput(); err != nil {
				t.Fatalf("systemctl stop: %v\n%s", err, out)
			}
			if tc.drained {
				if st := inst.buildStatus(t, key); st != "success" {
					t.Fatalf("KillMode=%s: build after stop is %q, want success", tc.mode, st)
				}
				return
			}
			// The step was signalled with the runner, so it cannot have
			// finished: the runner reports a failure, or died before
			// reporting and left the build running for the reaper.
			if st := inst.buildStatus(t, key); st == "success" {
				t.Fatalf("KillMode=%s: build reported success, so the stop test cannot tell the modes apart", tc.mode)
			}
		})
	}
}
