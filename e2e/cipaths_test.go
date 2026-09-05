package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A job with paths only builds when the push touched something it names.
// A doc-only push queues nothing; a push touching the named path queues
// the job, same as before path filters existed.
func TestCIPaths(t *testing.T) {
	inst := startInstance(t)
	aliceKey := inst.newKey(t, "alice")
	inst.admin(t, "admin", "user", "create", "alice", "--key", aliceKey+".pub")

	if _, errOut, code := inst.ssh(t, aliceKey, "", "repo", "create", "alice/app"); code != 0 {
		t.Fatalf("repo create: %s", errOut)
	}
	work := t.TempDir()
	env := inst.gitEnv(aliceKey)
	mustGit(t, work, env, "clone", inst.sshURL("alice/app"), "w")
	dir := filepath.Join(work, "w")
	os.MkdirAll(filepath.Join(dir, ".gitbay"), 0o755)
	os.MkdirAll(filepath.Join(dir, "src"), 0o755)
	os.MkdirAll(filepath.Join(dir, "docs"), 0o755)
	os.WriteFile(filepath.Join(dir, ".gitbay", "ci.yml"), []byte(
		"jobs:\n  unit:\n    paths:\n      - src/**\n    steps:\n      - echo fine\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "src", "x.go"), []byte("package x\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "docs", "x.md"), []byte("# x\n"), 0o644)
	mustGit(t, dir, env, "checkout", "-q", "-b", "main")
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, env, "commit", "-q", "-m", "base")
	mustGit(t, dir, env, "push", "-q", "origin", "main")
	// A new branch has no diff base, so the filter fails open: the base
	// push above already queued build 1.
	before := strings.Count(inst.buildList(t, aliceKey), "\n")
	if before != 1 {
		t.Fatalf("base push did not queue exactly one build:\n%s", inst.buildList(t, aliceKey))
	}

	// A push touching only docs does not match src/**: no build queued.
	os.WriteFile(filepath.Join(dir, "docs", "x.md"), []byte("# x changed\n"), 0o644)
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, env, "commit", "-q", "-m", "docs only")
	mustGit(t, dir, env, "push", "-q", "origin", "main")
	if out := inst.buildList(t, aliceKey); strings.Count(out, "\n") != before {
		t.Fatalf("doc-only push queued a build:\n%s", out)
	}

	// A push touching src matches: a build is queued.
	os.WriteFile(filepath.Join(dir, "src", "x.go"), []byte("package x\n\nvar y int\n"), 0o644)
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, env, "commit", "-q", "-m", "src change")
	mustGit(t, dir, env, "push", "-q", "origin", "main")
	out := inst.buildList(t, aliceKey)
	if strings.Count(out, "\n") != before+1 {
		t.Fatalf("src push did not queue a build:\n%s", out)
	}
	if !strings.Contains(out, "unit\tpending") {
		t.Fatalf("src push did not queue the unit job:\n%s", out)
	}
}

// A branch's first push has no old sha, but path filters must still
// apply: the normal workflow here is branch, commit, open an MR, and
// that first push is always a new branch. Without a merge-base fallback,
// a docs-only branch queues the full suite anyway (#171).
func TestCIPathsNewBranch(t *testing.T) {
	inst := startInstance(t)
	aliceKey := inst.newKey(t, "alice")
	inst.admin(t, "admin", "user", "create", "alice", "--key", aliceKey+".pub")

	if _, errOut, code := inst.ssh(t, aliceKey, "", "repo", "create", "alice/app"); code != 0 {
		t.Fatalf("repo create: %s", errOut)
	}
	work := t.TempDir()
	env := inst.gitEnv(aliceKey)
	mustGit(t, work, env, "clone", inst.sshURL("alice/app"), "w")
	dir := filepath.Join(work, "w")
	os.MkdirAll(filepath.Join(dir, ".gitbay"), 0o755)
	os.MkdirAll(filepath.Join(dir, "src"), 0o755)
	os.MkdirAll(filepath.Join(dir, "docs"), 0o755)
	os.WriteFile(filepath.Join(dir, ".gitbay", "ci.yml"), []byte(
		"jobs:\n  unit:\n    paths-ignore:\n      - docs/**\n    steps:\n      - echo fine\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "src", "x.go"), []byte("package x\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "docs", "x.md"), []byte("# x\n"), 0o644)
	mustGit(t, dir, env, "checkout", "-q", "-b", "main")
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, env, "commit", "-q", "-m", "base")
	mustGit(t, dir, env, "push", "-q", "origin", "main")
	before := strings.Count(inst.buildList(t, aliceKey), "\n")

	// A brand-new branch whose only commit touches an ignored path: the
	// filter must apply on this, its first push, not just on later ones.
	mustGit(t, dir, env, "checkout", "-q", "-b", "docs-branch")
	os.WriteFile(filepath.Join(dir, "docs", "x.md"), []byte("# x changed\n"), 0o644)
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, env, "commit", "-q", "-m", "docs only")
	mustGit(t, dir, env, "push", "-q", "origin", "docs-branch")
	if out := inst.buildList(t, aliceKey); strings.Count(out, "\n") != before {
		t.Fatalf("new branch's docs-only push queued a build:\n%s", out)
	}

	// A brand-new branch touching a matched path still queues, on its
	// first push.
	mustGit(t, dir, env, "checkout", "-q", "-b", "src-branch")
	os.WriteFile(filepath.Join(dir, "src", "x.go"), []byte("package x\n\nvar y int\n"), 0o644)
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, env, "commit", "-q", "-m", "src change")
	mustGit(t, dir, env, "push", "-q", "origin", "src-branch")
	out := inst.buildList(t, aliceKey)
	if strings.Count(out, "\n") != before+1 {
		t.Fatalf("new branch's src push did not queue a build:\n%s", out)
	}
	if !strings.Contains(out, "unit\tpending") {
		t.Fatalf("new branch's src push did not queue the unit job:\n%s", out)
	}
}
