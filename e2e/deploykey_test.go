package e2e

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDeployKeys(t *testing.T) {
	inst := startInstance(t)
	aliceKey := inst.newKey(t, "alice")
	bobKey := inst.newKey(t, "bob")
	inst.admin(t, "admin", "user", "create", "alice", "--key", aliceKey+".pub")
	inst.admin(t, "admin", "user", "create", "bob", "--key", bobKey+".pub")

	// Private repo with content.
	if _, errOut, code := inst.ssh(t, aliceKey, "", "repo", "create", "alice/app", "--private"); code != 0 {
		t.Fatalf("repo create: %s", errOut)
	}
	work := t.TempDir()
	env := inst.gitEnv(aliceKey)
	mustGit(t, work, env, "clone", inst.sshURL("alice/app"), "w")
	dir := filepath.Join(work, "w")
	os.WriteFile(filepath.Join(dir, "app.txt"), []byte("v1\n"), 0o644)
	mustGit(t, dir, env, "checkout", "-q", "-b", "main")
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, env, "commit", "-q", "-m", "init")
	mustGit(t, dir, env, "push", "-q", "origin", "main")

	// Bind a read-only deploy key; non-admins cannot.
	roKey := inst.newKey(t, "ci-ro")
	roPub, _ := os.ReadFile(roKey + ".pub")
	if _, _, code := inst.ssh(t, bobKey, string(roPub), "repo", "deploy-key", "add", "alice/app"); code != 3 {
		t.Fatalf("non-admin deploy-key add on private repo: want not-found parity")
	}
	out, errOut, code := inst.ssh(t, aliceKey, string(roPub), "repo", "deploy-key", "add", "alice/app", "--json")
	if code != 0 {
		t.Fatalf("deploy-key add: %s", errOut)
	}
	var env2 struct {
		Data struct {
			Fingerprint string `json:"fingerprint"`
		} `json:"data"`
	}
	json.Unmarshal([]byte(out), &env2)
	roFP := env2.Data.Fingerprint

	// The ro key clones the private repo but cannot push, cannot touch any
	// other repo, and cannot run control commands.
	roEnv := inst.gitEnv(roKey)
	roWork := t.TempDir()
	mustGit(t, roWork, roEnv, "clone", inst.sshURL("alice/app"), "w")
	roDir := filepath.Join(roWork, "w")
	mustGit(t, roDir, roEnv, "commit", "-q", "--allow-empty", "-m", "try")
	if out, code := gitRun(t, roDir, roEnv, "push", "origin", "main"); code == 0 {
		t.Fatalf("ro deploy key pushed:\n%s", out)
	}
	if _, _, code := inst.ssh(t, aliceKey, "", "repo", "create", "alice/other", "--private"); code != 0 {
		t.Fatal("other repo failed")
	}
	if out, code := gitRun(t, t.TempDir(), roEnv, "clone", inst.sshURL("alice/other")); code == 0 || !strings.Contains(out, "repository not found") {
		t.Fatalf("deploy key crossed repos: %d\n%s", code, out)
	}
	if _, _, code := inst.ssh(t, roKey, "", "whoami"); code != 4 {
		t.Fatal("deploy key ran a control command")
	}

	// An rw key pushes.
	rwKey := inst.newKey(t, "ci-rw")
	rwPub, _ := os.ReadFile(rwKey + ".pub")
	if _, errOut, code = inst.ssh(t, aliceKey, string(rwPub), "repo", "deploy-key", "add", "alice/app", "--rw"); code != 0 {
		t.Fatalf("rw add: %s", errOut)
	}
	rwEnv := inst.gitEnv(rwKey)
	rwWork := t.TempDir()
	mustGit(t, rwWork, rwEnv, "clone", inst.sshURL("alice/app"), "w")
	rwDir := filepath.Join(rwWork, "w")
	mustGit(t, rwDir, rwEnv, "commit", "-q", "--allow-empty", "-m", "ci push")
	mustGit(t, rwDir, rwEnv, "push", "-q", "origin", "main")

	// The binding survives an owner rename (keys bind to the repo ID).
	if _, errOut, code = inst.ssh(t, aliceKey, "", "org", "create", "moved"); code != 0 {
		t.Fatalf("org create: %s", errOut)
	}
	if _, errOut, code = inst.ssh(t, aliceKey, "", "repo", "transfer", "alice/app", "moved"); code != 0 {
		t.Fatalf("transfer: %s", errOut)
	}
	mustGit(t, t.TempDir(), roEnv, "clone", inst.sshURL("moved/app"))

	// List shows both with modes; removal severs access immediately.
	out, _, _ = inst.ssh(t, aliceKey, "", "repo", "deploy-key", "list", "moved/app")
	if !strings.Contains(out, "ro") || !strings.Contains(out, "rw") {
		t.Fatalf("deploy-key list: %s", out)
	}
	if _, errOut, code = inst.ssh(t, aliceKey, "", "repo", "deploy-key", "remove", "moved/app", roFP); code != 0 {
		t.Fatalf("remove: %s", errOut)
	}
	if out, code := gitRun(t, t.TempDir(), roEnv, "clone", inst.sshURL("moved/app")); code == 0 {
		t.Fatalf("removed deploy key still works:\n%s", out)
	}
}
