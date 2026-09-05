package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A repository with require_checks on and a job whose paths-ignore excludes
// a docs-only change used to be unmergeable: the push queued nothing, so the
// head carried no statuses at all, and the gate refuses that outright. The
// filtered job now records a "skipped" status instead, which the gate reads
// as green (#172).
func TestSkippedStatusSatisfiesRequireChecks(t *testing.T) {
	inst := startInstance(t)
	aliceKey := inst.newKey(t, "alice")
	inst.admin(t, "admin", "user", "create", "alice", "--key", aliceKey+".pub",
		"--email", "alice@example.test", "--verified")

	if _, errOut, code := inst.ssh(t, aliceKey, "", "repo", "create", "alice/app"); code != 0 {
		t.Fatalf("repo create: %s", errOut)
	}
	if _, errOut, code := inst.ssh(t, aliceKey, "", "repo", "settings", "require-checks", "alice/app", "on"); code != 0 {
		t.Fatalf("require-checks on: %s", errOut)
	}

	work := t.TempDir()
	env := inst.gitEnv(aliceKey)
	mustGit(t, work, env, "clone", inst.sshURL("alice/app"), "w")
	dir := filepath.Join(work, "w")
	os.MkdirAll(filepath.Join(dir, ".gitbay"), 0o755)
	os.MkdirAll(filepath.Join(dir, "docs"), 0o755)
	os.WriteFile(filepath.Join(dir, ".gitbay", "ci.yml"), []byte(
		"jobs:\n  unit:\n    paths-ignore:\n      - docs/**\n    steps:\n      - echo hi\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "docs", "x.md"), []byte("# x\n"), 0o644)
	mustGit(t, dir, env, "checkout", "-q", "-b", "main")
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, env, "commit", "-q", "-m", "base")
	mustGit(t, dir, env, "push", "-q", "origin", "main")

	// A docs-only feature branch: the job's paths-ignore excludes every
	// file this push touches.
	mustGit(t, dir, env, "checkout", "-q", "-b", "docs-fix")
	os.WriteFile(filepath.Join(dir, "docs", "x.md"), []byte("# x, fixed\n"), 0o644)
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, env, "commit", "-q", "-m", "fix a typo")
	mustGit(t, dir, env, "push", "-q", "origin", "docs-fix")
	headSHA := strings.TrimSpace(mustGit(t, dir, env, "rev-parse", "HEAD"))

	status, _, code := inst.ssh(t, aliceKey, "", "status", "list", "alice/app", headSHA)
	if code != 0 {
		t.Fatalf("status list: %s", status)
	}
	if !strings.Contains(status, "ci/unit") || !strings.Contains(status, "skipped") {
		t.Fatalf("no skipped ci/unit status on %s:\n%s", headSHA, status)
	}

	if _, errOut, code := inst.ssh(t, aliceKey, "", "mr", "create", "alice/app",
		"--source", "docs-fix", "--target", "main", "--title", "'fix a typo'"); code != 0 {
		t.Fatalf("mr create: %s", errOut)
	}

	// Before #172 this failed with "requires green checks and none were
	// reported": the filtered push left the head with no statuses at all.
	if out, errOut, code := inst.ssh(t, aliceKey, "", "mr", "merge", "alice/app", "1",
		"--strategy", "merge"); code != 0 {
		t.Fatalf("mr merge refused a docs-only change under require_checks: %s %s", out, errOut)
	}
}
