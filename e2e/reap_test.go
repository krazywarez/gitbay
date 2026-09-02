package e2e

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A build claimed by a runner that never reports is failed by the
// scheduler's tick, with no runner alive to trigger it.
func TestStaleBuildReapedWithoutRunner(t *testing.T) {
	t.Setenv("GITBAY_SCHED_TICK", "500ms")
	t.Setenv("GITBAY_STALE_BUILD_DEADLINE", "2s")
	inst := startInstance(t)
	aliceKey := inst.newKey(t, "alice")
	runnerKey := inst.newKey(t, "ci")
	inst.admin(t, "admin", "user", "create", "alice", "--key", aliceKey+".pub")
	inst.admin(t, "admin", "user", "create", "ci", "--key", runnerKey+".pub", "--admin")
	if _, _, code := inst.ssh(t, aliceKey, "", "repo", "create", "alice/app"); code != 0 {
		t.Fatal("repo create failed")
	}
	work := t.TempDir()
	env := inst.gitEnv(aliceKey)
	mustGit(t, work, env, "clone", inst.sshURL("alice/app"), "w")
	dir := filepath.Join(work, "w")
	os.MkdirAll(filepath.Join(dir, ".gitbay"), 0o755)
	os.WriteFile(filepath.Join(dir, ".gitbay", "ci.yml"), []byte("jobs:\n  ok:\n    steps:\n      - echo fine\n"), 0o644)
	mustGit(t, dir, env, "checkout", "-q", "-b", "main")
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, env, "commit", "-q", "-m", "ci")
	mustGit(t, dir, env, "push", "-q", "origin", "main")

	// Claim the build the way a runner does, then vanish.
	out, errOut, code := inst.ssh(t, runnerKey, "", "runner", "next", "--json")
	if code != 0 || !strings.Contains(out, `"job":"ok"`) {
		t.Fatalf("runner next: exit %d\n%s%s", code, out, errOut)
	}
	var claim struct {
		Data struct {
			SHA string `json:"sha"`
		} `json:"data"`
	}
	json.Unmarshal([]byte(out), &claim)

	status := func() (string, string) {
		out, _, _ := inst.ssh(t, aliceKey, "", "build", "list", "alice/app", "--json")
		var env struct {
			Data []struct {
				Status string `json:"status"`
			} `json:"data"`
		}
		json.Unmarshal([]byte(out), &env)
		st := ""
		if len(env.Data) > 0 {
			st = env.Data[0].Status
		}
		cs, _, _ := inst.ssh(t, aliceKey, "", "status", "list", "alice/app", claim.Data.SHA)
		return st, cs
	}
	if st, _ := status(); st != "running" {
		t.Fatalf("claimed build is %q, want running", st)
	}
	deadline := time.Now().Add(20 * time.Second)
	for {
		st, cs := status()
		if st == "failure" && strings.Contains(cs, "build abandoned") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("never reaped: build %q, statuses:\n%s", st, cs)
		}
		time.Sleep(250 * time.Millisecond)
	}
	if out, _, _ := inst.ssh(t, aliceKey, "", "build", "log", "alice/app", "1"); !strings.Contains(out, "build abandoned") {
		t.Fatalf("log lacks the abandonment note:\n%s", out)
	}
}

func TestAdminRunners(t *testing.T) {
	inst := startInstance(t)
	rootKey := inst.newKey(t, "root")
	aliceKey := inst.newKey(t, "alice")
	runnerKey := inst.newKey(t, "ci")
	inst.admin(t, "admin", "user", "create", "root", "--key", rootKey+".pub", "--admin")
	inst.admin(t, "admin", "user", "create", "alice", "--key", aliceKey+".pub")
	inst.admin(t, "admin", "user", "create", "ci", "--key", runnerKey+".pub", "--admin")
	if _, _, code := inst.ssh(t, aliceKey, "", "admin", "runners"); code != 4 {
		t.Fatal("non-admin listed runners")
	}
	if out, _, code := inst.ssh(t, rootKey, "", "admin", "runners", "--json"); code != 0 || strings.TrimSpace(out) != `{"protocol_version":1,"data":[]}` {
		t.Fatalf("no runners yet: exit %d %s", code, out)
	}
	if _, _, code := inst.ssh(t, aliceKey, "", "repo", "create", "alice/app"); code != 0 {
		t.Fatal("repo create failed")
	}
	// An idle poll registers the runner with its scope.
	if _, _, code := inst.ssh(t, runnerKey, "", "runner", "next", "alice/app"); code != 0 {
		t.Fatal("runner next failed")
	}
	out, _, _ := inst.ssh(t, rootKey, "", "admin", "runners")
	if !strings.HasPrefix(out, "ci\t") || !strings.Contains(out, "\talice/app\tidle") {
		t.Fatalf("idle runner row:\n%s", out)
	}
	work := t.TempDir()
	env := inst.gitEnv(aliceKey)
	mustGit(t, work, env, "clone", inst.sshURL("alice/app"), "w")
	dir := filepath.Join(work, "w")
	os.MkdirAll(filepath.Join(dir, ".gitbay"), 0o755)
	os.WriteFile(filepath.Join(dir, ".gitbay", "ci.yml"), []byte("jobs:\n  ok:\n    steps:\n      - echo fine\n"), 0o644)
	mustGit(t, dir, env, "checkout", "-q", "-b", "main")
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, env, "commit", "-q", "-m", "ci")
	mustGit(t, dir, env, "push", "-q", "origin", "main")
	out, _, code := inst.ssh(t, runnerKey, "", "runner", "next", "--json")
	if code != 0 || !strings.Contains(out, `"job":"ok"`) {
		t.Fatalf("claim: %s", out)
	}
	var claim struct {
		Data struct {
			ID int64 `json:"id"`
		} `json:"data"`
	}
	json.Unmarshal([]byte(out), &claim)
	if out, _, _ := inst.ssh(t, rootKey, "", "admin", "runners"); !strings.Contains(out, "\tany\talice/app #1 ok since ") {
		t.Fatalf("holding runner row:\n%s", out)
	}
	if _, _, code := inst.ssh(t, runnerKey, "", "runner", "done", fmt.Sprint(claim.Data.ID), "success"); code != 0 {
		t.Fatal("runner done failed")
	}
	if out, _, _ := inst.ssh(t, rootKey, "", "admin", "runners"); !strings.Contains(out, "\tany\tidle") {
		t.Fatalf("runner still holds a build after done:\n%s", out)
	}
	// Host-local, the same read.
	if out := inst.admin(t, "admin", "runners", "--json"); !strings.Contains(out, `"username":"ci"`) {
		t.Fatalf("host runners:\n%s", out)
	}
}

// /healthz is unauthenticated, cache-free, and says which build serves.
func TestHealthz(t *testing.T) {
	inst := startInstance(t)
	status, body := inst.get(t, "/healthz")
	if status != 200 || !strings.Contains(body, `"ok":true`) || !strings.Contains(body, `"commit":"`) {
		t.Fatalf("healthz: %d %s", status, body)
	}
	// The name is reserved: no account can shadow the route.
	if _, errOut, code := inst.ssh(t, inst.newKey(t, "x"), "", "register", "--username", "healthz"); code == 0 {
		t.Fatalf("healthz registered as a username: %s", errOut)
	}
}

// gc --lfs removes objects no pointer names, keeps referenced ones, and
// leaves anything young enough to be an upload ahead of its push.
func TestGCLFSOrphans(t *testing.T) {
	inst := startInstance(t)
	aliceKey := inst.newKey(t, "alice")
	inst.admin(t, "admin", "user", "create", "alice", "--key", aliceKey+".pub")
	if _, _, code := inst.ssh(t, aliceKey, "", "repo", "create", "alice/big"); code != 0 {
		t.Fatal("repo create failed")
	}
	put := func(content string, age time.Duration) string {
		t.Helper()
		sum := sha256.Sum256([]byte(content))
		oid := hex.EncodeToString(sum[:])
		path := filepath.Join(inst.root, "lfs", oid[:2], oid[2:4], oid)
		os.MkdirAll(filepath.Dir(path), 0o755)
		os.WriteFile(path, []byte(content), 0o644)
		os.Chtimes(path, time.Now().Add(-age), time.Now().Add(-age))
		return oid
	}
	kept := put("referenced payload", 48*time.Hour)
	orphan := put("nobody points here", 48*time.Hour)
	young := put("just uploaded", time.Minute)

	work := t.TempDir()
	env := inst.gitEnv(aliceKey)
	mustGit(t, work, env, "clone", inst.sshURL("alice/big"), "w")
	dir := filepath.Join(work, "w")
	pointer := fmt.Sprintf("version https://git-lfs.github.com/spec/v1\noid sha256:%s\nsize %d\n", kept, len("referenced payload"))
	os.WriteFile(filepath.Join(dir, "data.bin"), []byte(pointer), 0o644)
	mustGit(t, dir, env, "checkout", "-q", "-b", "main")
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, env, "commit", "-q", "-m", "pointer")
	mustGit(t, dir, env, "push", "-q", "origin", "main")

	if out := inst.admin(t, "admin", "stats", "--json"); !strings.Contains(out, `"lfs_bytes":`) || strings.Contains(out, `"lfs_bytes":0`) {
		t.Fatalf("stats lfs bytes:\n%s", out)
	}
	out := inst.admin(t, "admin", "gc", "--lfs")
	if !strings.Contains(out, "lfs\t1 referenced, removed 1 orphans") {
		t.Fatalf("gc --lfs:\n%s", out)
	}
	exists := func(oid string) bool {
		_, err := os.Stat(filepath.Join(inst.root, "lfs", oid[:2], oid[2:4], oid))
		return err == nil
	}
	if !exists(kept) || exists(orphan) || !exists(young) {
		t.Fatalf("after gc: kept=%v orphan=%v young=%v", exists(kept), exists(orphan), exists(young))
	}
}
