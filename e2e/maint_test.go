package e2e

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAdminGCAndStats(t *testing.T) {
	inst := startInstance(t)
	aliceKey := inst.newKey(t, "alice")
	inst.admin(t, "admin", "user", "create", "alice", "--key", aliceKey+".pub")

	if _, errOut, code := inst.ssh(t, aliceKey, "", "repo", "create", "alice/app"); code != 0 {
		t.Fatalf("repo create: %s", errOut)
	}
	if _, _, code := inst.ssh(t, aliceKey, "", "repo", "create", "alice/other"); code != 0 {
		t.Fatal("second repo create failed")
	}
	if _, _, code := inst.ssh(t, aliceKey, "", "issue", "create", "alice/app", "--title", "'x'"); code != 0 {
		t.Fatal("issue create failed")
	}

	// Push several commits so the repo has loose objects worth packing.
	work := t.TempDir()
	env := inst.gitEnv(aliceKey)
	mustGit(t, work, env, "clone", inst.sshURL("alice/app"), "w")
	dir := filepath.Join(work, "w")
	mustGit(t, dir, env, "checkout", "-q", "-b", "main")
	for i := 0; i < 10; i++ {
		os.WriteFile(filepath.Join(dir, "f.txt"), []byte(strings.Repeat(fmt.Sprint(i), 2000)+"\n"), 0o644)
		mustGit(t, dir, env, "add", ".")
		mustGit(t, dir, env, "commit", "-q", "-m", fmt.Sprintf("c%d", i))
	}
	mustGit(t, dir, env, "push", "-q", "origin", "main")

	// Stats: counts and per-repo disk usage.
	out := inst.admin(t, "admin", "stats")
	if !strings.Contains(out, "repos 2") || !strings.Contains(out, "issues 1 (1 open)") ||
		!strings.Contains(out, "alice/app") || !strings.Contains(out, "alice/other") {
		t.Fatalf("stats output: %s", out)
	}
	if out = inst.admin(t, "admin", "stats", "--json"); !strings.Contains(out, `"repos":2`) ||
		!strings.Contains(out, `"path":"alice/app"`) {
		t.Fatalf("stats json: %s", out)
	}

	// GC one repo, then all; the repo must survive (clone still works).
	out = inst.admin(t, "admin", "gc", "--repo", "alice/app")
	if !strings.Contains(out, "alice/app") || !strings.Contains(out, "total") ||
		strings.Contains(out, "alice/other") {
		t.Fatalf("scoped gc: %s", out)
	}
	if out = inst.admin(t, "admin", "gc"); !strings.Contains(out, "alice/other") {
		t.Fatalf("full gc: %s", out)
	}
	if out = inst.forgedAdminErr(t, "admin", "gc", "--repo", "alice/nope"); !strings.Contains(out, "no repository") {
		t.Fatalf("bogus repo: %s", out)
	}
	work2 := t.TempDir()
	mustGit(t, work2, env, "clone", "-q", inst.sshURL("alice/app"), "r")
	if data, err := os.ReadFile(filepath.Join(work2, "r", "f.txt")); err != nil || len(data) == 0 {
		t.Fatal("repo damaged by gc")
	}
}
