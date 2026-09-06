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

	env := stepEnv(job{Repo: "alice/app", SHA: "abc", Ref: "main", Job: "test"}, "/tmp/ws")

	for _, e := range env {
		if strings.HasPrefix(e, "GITBAY_RUNNER_TOKEN=") || strings.HasPrefix(e, "AWS_SECRET_ACCESS_KEY=") {
			t.Errorf("the runner's own environment reached a build step: %q", e)
		}
	}
	want := map[string]string{
		"CI": "true", "GITBAY_REPO": "alice/app", "GITBAY_SHA": "abc",
		"GITBAY_REF": "main", "GITBAY_JOB": "test",
		// HOME is the workspace so a build cannot read the runner's
		// dotfiles, where tools keep credentials.
		"HOME": "/tmp/ws",
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
	env := stepEnv(job{Secrets: map[string]string{"TOKEN": "s3cret"}}, "/tmp/ws")
	if !containsEnv(env, "TOKEN=s3cret") {
		t.Error("a trusted build's secret did not reach the step")
	}
	env = stepEnv(job{}, "/tmp/ws")
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
	if env := stepEnv(job{}, "/tmp/ws"); !containsEnv(env, "PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin") {
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
