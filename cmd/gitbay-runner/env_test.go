package main

import (
	"os"
	"strings"
	"testing"
)

// A step's environment is constructed, not inherited: repository content
// must not see what the operator set on the runner service (#144).
func TestStepEnvDoesNotInherit(t *testing.T) {
	t.Setenv("GITBAY_RUNNER_TOKEN", "a-secret-the-service-was-given")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "also-not-for-builds")

	env := stepEnv(job{Repo: "alice/app", SHA: "abc", Ref: "main", Job: "test"}, "/tmp/buildhome")

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
	env := stepEnv(job{Secrets: map[string]string{"TOKEN": "s3cret"}}, "/tmp/buildhome")
	if !containsEnv(env, "TOKEN=s3cret") {
		t.Error("a trusted build's secret did not reach the step")
	}
	env = stepEnv(job{}, "/tmp/buildhome")
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
	if env := stepEnv(job{}, "/tmp/buildhome"); !containsEnv(env, "PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin") {
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
	env := stepEnv(job{ID: 7}, "/var/lib/gitbay-runner/work/home")
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
