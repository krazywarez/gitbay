package main

import (
	"os"
	"strings"
	"testing"
	"time"
)

// A step's environment is constructed, not inherited: repository content
// must not see what the operator set on the runner service (#144).
func TestStepEnvDoesNotInherit(t *testing.T) {
	t.Setenv("GITBAY_RUNNER_TOKEN", "a-secret-the-service-was-given")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "also-not-for-builds")

	env := stepEnv(job{Repo: "alice/app", SHA: "abc", Ref: "main", Job: "test"}, "/tmp/buildhome", "git@x.test")

	for _, e := range env {
		if strings.HasPrefix(e, "GITBAY_RUNNER_TOKEN=") || strings.HasPrefix(e, "AWS_SECRET_ACCESS_KEY=") {
			t.Errorf("the runner's own environment reached a build step: %q", e)
		}
	}
	want := map[string]string{
		"CI": "true", "GITBAY_REPO": "alice/app", "GITBAY_SHA": "abc",
		"GITBAY_REF": "main", "GITBAY_JOB": "test",
		// HOME is the shared build home, not the runner's own, so a
		// build cannot read the dotfiles where tools keep credentials —
		// and not the workspace, which is deleted after every build,
		// taking every tool cache with it.
		"HOME": "/tmp/buildhome",
	}
	got := map[string]string{}
	for _, e := range env {
		k, v, _ := strings.Cut(e, "=")
		got[k] = v
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
	if got["PATH"] == "" {
		t.Error("PATH is empty; a step could not find any tool")
	}
}

// Secrets are passed through when the server sent them, which it does
// only for a trusted build.
func TestStepEnvCarriesSecrets(t *testing.T) {
	env := stepEnv(job{Secrets: map[string]string{"TOKEN": "s3cret"}}, "/tmp/buildhome", "git@x.test")
	if !containsEnv(env, "TOKEN=s3cret") {
		t.Error("a trusted build's secret did not reach the step")
	}
	env = stepEnv(job{}, "/tmp/buildhome", "git@x.test")
	for _, e := range env {
		if strings.HasPrefix(e, "TOKEN=") {
			t.Errorf("a secret appeared with none sent: %q", e)
		}
	}
}

// PATH falls back rather than leaving a step unable to find anything.
func TestStepEnvPathFallback(t *testing.T) {
	old := os.Getenv("PATH")
	os.Unsetenv("PATH")
	defer os.Setenv("PATH", old)
	if env := stepEnv(job{}, "/tmp/buildhome", "git@x.test"); !containsEnv(env, "PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin") {
		t.Errorf("no PATH fallback: %v", env)
	}
}

func containsEnv(env []string, want string) bool {
	for _, e := range env {
		if e == want {
			return true
		}
	}
	return false
}

// The build home must outlive a build. It was briefly the workspace,
// which run() removes when the build ends, so every build re-downloaded
// the Go module cache and the ~50MB sonar scanner.
func TestStepEnvHomeIsNotTheWorkspace(t *testing.T) {
	env := stepEnv(job{ID: 7}, "/var/lib/gitbay-runner/work/home", "git@x.test")
	for _, e := range env {
		if strings.HasPrefix(e, "HOME=") && strings.Contains(e, "build-7") {
			t.Errorf("HOME is the per-build workspace, which is deleted after the build: %q", e)
		}
	}
}

// podman runs from a system service, where the systemd cgroup manager
// has no user slice to work in. Every invocation must say so, or crun
// fails creating the container's scope (#144).
func TestPodmanUsesCgroupfs(t *testing.T) {
	r := &runner{}
	got := r.podmanGlobal()
	found := false
	for _, f := range got {
		if f == "--cgroup-manager=cgroupfs" {
			found = true
		}
	}
	if !found {
		t.Errorf("podmanGlobal() = %v, missing the cgroupfs manager", got)
	}
}

// The build home is where caches live, so the container must see it at
// the path HOME names; otherwise every containerised build starts cold.
func TestEnvHomeFindsHome(t *testing.T) {
	if got := envHome([]string{"PATH=/bin", "HOME=/var/lib/gitbay-runner/work/home", "CI=true"}); got != "/var/lib/gitbay-runner/work/home" {
		t.Errorf("envHome = %q", got)
	}
	if got := envHome([]string{"PATH=/bin"}); got != "" {
		t.Errorf("envHome with no HOME = %q, want empty", got)
	}
}

// Closing stop drains: the build in flight finishes and is reported, and
// no further build is claimed (#179).
func TestServeDrainsOnStop(t *testing.T) {
	stop := make(chan struct{})
	started := make(chan struct{})
	release := make(chan struct{})
	calls := 0
	r := &runner{stepFn: func() (bool, error) {
		calls++
		if calls == 1 {
			close(started)
			<-release // the build is in flight while stop closes
		}
		return true, nil
	}}
	done := make(chan struct{})
	go func() { r.serve(1, false, time.Millisecond, stop); close(done) }()
	<-started
	close(stop)
	close(release)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("serve did not return after the in-flight build finished")
	}
	if calls != 1 {
		t.Errorf("claimed %d builds after stop, want the one already in flight", calls-1)
	}
}

// An idle worker leaves promptly on stop rather than sleeping out a poll.
func TestServeStopsWhileIdle(t *testing.T) {
	stop := make(chan struct{})
	r := &runner{stepFn: func() (bool, error) { return false, nil }}
	done := make(chan struct{})
	go func() { r.serve(1, false, time.Hour, stop); close(done) }()
	time.Sleep(20 * time.Millisecond)
	close(stop)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("idle worker did not stop")
	}
}

// A private key is a secret with newlines. An env file cannot carry one,
// so such values reach the container through podman's own environment,
// named on the command line and never valued there.
func TestSplitEnvKeepsMultilineOutOfTheFile(t *testing.T) {
	env := []string{"PATH=/bin", "KEY=-----BEGIN\nabc\n-----END", "TOKEN=s3cret", "CR=a\rb"}
	file, inherit := splitEnv(env)
	if len(file) != 2 || file[0] != "PATH=/bin" || file[1] != "TOKEN=s3cret" {
		t.Fatalf("file env = %q", file)
	}
	if len(inherit) != 2 || inherit[0] != "KEY=-----BEGIN\nabc\n-----END" || inherit[1] != "CR=a\rb" {
		t.Fatalf("inherited env = %q", inherit)
	}
	args := inheritArgs(inherit)
	want := []string{"--env", "KEY", "--env", "CR"}
	if strings.Join(args, " ") != strings.Join(want, " ") {
		t.Fatalf("podman args = %q, want %q", args, want)
	}
	for _, a := range args {
		if strings.Contains(a, "BEGIN") || strings.Contains(a, "a\rb") {
			t.Fatalf("a secret's value reached argv: %q", a)
		}
	}
}

// A build that talks back to the instance — releases, comments — needs an
// address that works from where it runs. GITBAY_SSH carries the runner's
// remote; under podman a loopback remote is rewritten to the address at
// which pasta exposes the host, since the host's own addresses belong to
// the container inside it.
func TestStepEnvCarriesInstanceAddress(t *testing.T) {
	env := stepEnv(job{}, "/tmp/buildhome", "git@gitbay.org")
	if !containsEnv(env, "GITBAY_SSH=git@gitbay.org") {
		t.Errorf("GITBAY_SSH missing: %q", env)
	}
	for _, tc := range []struct{ remote, isolation, want string }{
		{"git@127.0.0.1", isolationNone, "git@127.0.0.1"},
		{"git@127.0.0.1", isolationPodman, "git@169.254.1.2"},
		{"git@localhost", isolationPodman, "git@169.254.1.2"},
		{"git@gitbay.org", isolationPodman, "git@gitbay.org"},
		{"gitbay.org", isolationPodman, "gitbay.org"},
	} {
		r := &runner{remote: tc.remote, isolation: tc.isolation}
		if got := r.buildSSH(); got != tc.want {
			t.Errorf("remote %s under %s: got %s want %s", tc.remote, tc.isolation, got, tc.want)
		}
	}
}
