package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOrganizations(t *testing.T) {
	inst := startInstance(t)
	aliceKey := inst.newKey(t, "alice")
	bobKey := inst.newKey(t, "bob")
	eveKey := inst.newKey(t, "eve")
	inst.admin(t, "admin", "user", "create", "alice",
		"--key", aliceKey+".pub", "--email", "alice@example.test", "--verified")
	inst.admin(t, "admin", "user", "create", "bob", "--key", bobKey+".pub")
	inst.admin(t, "admin", "user", "create", "eve", "--key", eveKey+".pub")

	// Alice creates an org; the namespace is shared with users.
	if _, errOut, code := inst.ssh(t, aliceKey, "", "org", "create", "krz"); code != 0 {
		t.Fatalf("org create: %s", errOut)
	}
	if _, _, code := inst.ssh(t, aliceKey, "", "org", "create", "bob"); code == 0 {
		t.Fatal("org created with a user's name")
	}
	if out := inst.admin(t, "admin", "user", "create", "krz2", "--key", inst.newKey(t, "krz2")+".pub"); out == "" {
		t.Fatal("control user create failed")
	}
	// A user cannot claim an org's name either.
	cmd := inst.forgedAdminErr(t, "admin", "user", "create", "krz")
	if !strings.Contains(cmd, "taken") {
		t.Fatalf("user with org name: %s", cmd)
	}

	// Only org admins create repos under the org.
	if _, errOut, code := inst.ssh(t, bobKey, "", "repo", "create", "krz/lib"); code != 4 {
		t.Fatalf("non-member org repo create: %d %s", code, errOut)
	}
	if _, errOut, code := inst.ssh(t, aliceKey, "", "repo", "create", "krz/lib", "--private"); code != 0 {
		t.Fatalf("org repo create: %s", errOut)
	}

	// Membership-derived access: bob (member) gets write, eve (outsider)
	// sees nothing on the private repo.
	if _, errOut, code := inst.ssh(t, aliceKey, "", "org", "members", "add", "krz", "bob"); code != 0 {
		t.Fatalf("members add: %s", errOut)
	}
	work := t.TempDir()
	bobEnv := inst.gitEnv(bobKey)
	mustGit(t, work, bobEnv, "clone", inst.sshURL("krz/lib"), "w")
	dir := filepath.Join(work, "w")
	os.WriteFile(filepath.Join(dir, "f.txt"), []byte("org work\n"), 0o644)
	mustGit(t, dir, bobEnv, "checkout", "-q", "-b", "main")
	mustGit(t, dir, bobEnv, "add", ".")
	mustGit(t, dir, bobEnv, "commit", "-q", "-m", "bob pushes to org repo")
	mustGit(t, dir, bobEnv, "push", "-q", "origin", "main")

	if out, code := gitRun(t, t.TempDir(), inst.gitEnv(eveKey), "clone", inst.sshURL("krz/lib")); code == 0 || !strings.Contains(out, "repository not found") {
		t.Fatalf("outsider on private org repo: %d\n%s", code, out)
	}

	// Members are not repo admins: bob cannot change settings or grant
	// access; an org admin can.
	if _, _, code := inst.ssh(t, bobKey, "", "repo", "settings", "protect", "krz/lib", "main"); code != 4 {
		t.Fatal("member changed org repo settings")
	}
	if _, _, code := inst.ssh(t, bobKey, "", "org", "members", "add", "krz", "eve"); code != 4 {
		t.Fatal("member managed org membership")
	}
	if _, errOut, code := inst.ssh(t, aliceKey, "", "repo", "settings", "protect", "krz/lib", "main"); code != 0 {
		t.Fatalf("org admin protect: %s", errOut)
	}

	// Explicit per-repo grants still work alongside membership: eve gets
	// read on the private org repo.
	if _, errOut, code := inst.ssh(t, aliceKey, "", "repo", "access", "grant", "krz/lib", "eve", "read"); code != 0 {
		t.Fatalf("grant: %s", errOut)
	}
	mustGit(t, t.TempDir(), inst.gitEnv(eveKey), "clone", inst.sshURL("krz/lib"))

	// Org repos list for members; org shows in org list.
	out, _, _ := inst.ssh(t, bobKey, "", "repo", "list")
	if !strings.Contains(out, "krz/lib") {
		t.Fatalf("member repo list missing org repo:\n%s", out)
	}
	out, _, _ = inst.ssh(t, bobKey, "", "org", "list")
	if !strings.Contains(out, "krz\tmember") {
		t.Fatalf("org list: %s", out)
	}

	// Promotion works; the last admin is protected.
	if _, errOut, code := inst.ssh(t, aliceKey, "", "org", "members", "add", "krz", "bob", "--role", "admin"); code != 0 {
		t.Fatalf("promote: %s", errOut)
	}
	if _, errOut, code := inst.ssh(t, bobKey, "", "org", "members", "remove", "krz", "alice"); code != 0 {
		t.Fatalf("bob (now admin) removing alice: %s", errOut)
	}
	_, errOut, code := inst.ssh(t, bobKey, "", "org", "members", "remove", "krz", "bob")
	if code != 2 || !strings.Contains(errOut, "at least one admin") {
		t.Fatalf("last admin removal: %d %s", code, errOut)
	}

	// Org deletion refuses while repos exist, then succeeds.
	_, errOut, code = inst.ssh(t, bobKey, "", "org", "delete", "krz", "--yes")
	if code != 1 || !strings.Contains(errOut, "still owns") {
		t.Fatalf("delete with repos: %d %s", code, errOut)
	}
	if _, errOut, code = inst.ssh(t, bobKey, "", "repo", "delete", "krz/lib", "--yes"); code != 0 {
		t.Fatalf("org repo delete: %s", errOut)
	}
	if _, errOut, code = inst.ssh(t, bobKey, "", "org", "delete", "krz", "--yes"); code != 0 {
		t.Fatalf("org delete: %s", errOut)
	}

	// Public org repos appear on the anonymous web index.
	if _, _, code = inst.ssh(t, aliceKey, "", "org", "create", "puborg"); code != 0 {
		t.Fatal("org create failed")
	}
	if _, _, code = inst.ssh(t, aliceKey, "", "repo", "create", "puborg/site"); code != 0 {
		t.Fatal("org public repo failed")
	}
	status, body := inst.get(t, "/")
	if status != 200 || !strings.Contains(body, "puborg/site") {
		t.Fatalf("org repo missing from index: %d", status)
	}
	if status, _ := inst.get(t, "/puborg/site"); status != 200 {
		t.Fatalf("org repo page: %d", status)
	}
}
