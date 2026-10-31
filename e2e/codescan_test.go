package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The instance's code scan end to end: enable, queue, a real runner runs
// the configured steps and uploads their SARIF, and the findings land in
// an issue. A results directory committed to the repository is cleared
// before the steps run, so it is never uploaded as the scan's (#340).
func TestSecurityScan(t *testing.T) {
	t.Parallel()
	inst := startInstanceWith(t, `[security]
image = "localhost/scan:1"
steps = ['mkdir -p "$GITBAY_RESULTS"', 'cp scan.sarif "$GITBAY_RESULTS/scan.sarif"']
`)
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
	mustGit(t, work, env, "clone", "-q", inst.sshURL("alice/app"), "w")
	dir := filepath.Join(work, "w")
	os.MkdirAll(filepath.Join(dir, ".gitbay-results"), 0o755)
	os.WriteFile(filepath.Join(dir, "scan.sarif"), []byte(`{"version":"2.1.0","runs":[{"tool":{"driver":{"name":"opengrep"}},
		"results":[{"ruleId":"python.exec-used","level":"error","message":{"text":"exec on input"},
		"locations":[{"physicalLocation":{"artifactLocation":{"uri":"app.py"},"region":{"startLine":7}}}]}]}]}`), 0o644)
	os.WriteFile(filepath.Join(dir, ".gitbay-results", "planted.sarif"), []byte(`{"runs":[{"tool":{"driver":{"name":"planted"}},
		"results":[{"ruleId":"planted.rule","message":{"text":"planted"}}]}]}`), 0o644)
	mustGit(t, dir, env, "checkout", "-q", "-b", "main")
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, env, "commit", "-q", "-m", "base")
	mustGit(t, dir, env, "push", "-q", "origin", "main")

	if _, errOut, code := inst.ssh(t, aliceKey, "", "repo", "security", "enable", "alice/app"); code != 0 {
		t.Fatalf("security enable: %s", errOut)
	}
	if _, errOut, code := inst.ssh(t, aliceKey, "", "repo", "security", "run", "alice/app"); code != 0 {
		t.Fatalf("security run: %s", errOut)
	}
	if out := inst.runnerOnce(t, runnerKey); !strings.Contains(out, "success") {
		t.Fatalf("runner:\n%s", out)
	}
	out, errOut, code := inst.ssh(t, aliceKey, "", "repo", "security", "status", "alice/app", "--json")
	if code != 0 || !strings.Contains(out, `"rule":"python.exec-used"`) || strings.Contains(out, "planted") {
		t.Fatalf("status: %d %s\n%s", code, errOut, out)
	}
	out, _, _ = inst.ssh(t, aliceKey, "", "issue", "list", "alice/app")
	if !strings.Contains(out, "Code scanning findings") {
		t.Fatalf("no findings issue:\n%s", out)
	}
	// The scan sets no commit status.
	sha := strings.TrimSpace(mustGit(t, dir, env, "rev-parse", "HEAD"))
	if out, _, _ := inst.ssh(t, aliceKey, "", "status", "list", "alice/app", sha); strings.Contains(out, "gitbay-security") {
		t.Fatalf("scan set a status:\n%s", out)
	}
}

// A runner attached for the code scan only leaves the repository's own
// jobs to its other runner: the shape of a repository whose job must run
// somewhere the scan image is not.
func TestRunnerAttachedForJobs(t *testing.T) {
	t.Parallel()
	inst := startInstanceWith(t, `[security]
image = "localhost/scan:1"
steps = ['mkdir -p "$GITBAY_RESULTS"', 'cp empty.sarif "$GITBAY_RESULTS/x.sarif"']
`)
	inst.runner = buildRunner(t)
	aliceKey := inst.newKey(t, "alice")
	inst.admin(t, "admin", "user", "create", "alice", "--key", aliceKey+".pub")
	scanKey := inst.newKey(t, "scanner")
	if _, errOut, code := inst.ssh(t, aliceKey, "", "repo", "create", "alice/app"); code != 0 {
		t.Fatalf("repo create: %s", errOut)
	}
	pub, err := os.ReadFile(scanKey + ".pub")
	if err != nil {
		t.Fatal(err)
	}
	if _, errOut, code := inst.ssh(t, aliceKey, string(pub), "repo", "runner", "add", "alice/app", "--jobs", "gitbay-security"); code != 0 {
		t.Fatalf("runner add --jobs: %s", errOut)
	}
	work := t.TempDir()
	env := inst.gitEnv(aliceKey)
	mustGit(t, work, env, "clone", "-q", inst.sshURL("alice/app"), "w")
	dir := filepath.Join(work, "w")
	os.MkdirAll(filepath.Join(dir, ".gitbay"), 0o755)
	os.WriteFile(filepath.Join(dir, ".gitbay", "ci.yml"), []byte("jobs:\n  pull:\n    steps:\n      - echo pulled\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "empty.sarif"), []byte(`{"version":"2.1.0","runs":[]}`), 0o644)
	mustGit(t, dir, env, "checkout", "-q", "-b", "main")
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, env, "commit", "-q", "-m", "ci")
	mustGit(t, dir, env, "push", "-q", "origin", "main")
	inst.ssh(t, aliceKey, "", "repo", "security", "enable", "alice/app")
	if _, errOut, code := inst.ssh(t, aliceKey, "", "repo", "security", "run", "alice/app"); code != 0 {
		t.Fatalf("security run: %s", errOut)
	}

	// The pull build is older, but this runner may only take the scan.
	if out := inst.runnerOnce(t, scanKey); !strings.Contains(out, "gitbay-security") || strings.Contains(out, " pull ") {
		t.Fatalf("runner:\n%s", out)
	}
	if out := inst.runnerOnce(t, scanKey); strings.Contains(out, "pull") {
		t.Fatalf("the scan runner claimed the pull job:\n%s", out)
	}
	out, _, _ := inst.ssh(t, aliceKey, "", "build", "list", "alice/app")
	if !strings.Contains(out, "pull\tpending") {
		t.Fatalf("pull was not left pending:\n%s", out)
	}
}
