package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Removing a member from an org ends the access they held through its
// teams (#196). Before the fix, team_members rows survived removal, so a
// former member kept pushing.
func TestOrgMemberRemovalEndsTeamAccess(t *testing.T) {
	inst := startInstance(t)
	aliceKey := inst.newKey(t, "alice")
	bobKey := inst.newKey(t, "bob")
	inst.admin(t, "admin", "user", "create", "alice", "--key", aliceKey+".pub")
	inst.admin(t, "admin", "user", "create", "bob", "--key", bobKey+".pub")

	for _, args := range [][]string{
		{"org", "create", "acme"},
		{"org", "settings", "members-role", "acme", "none"},
		{"org", "members", "add", "acme", "bob"},
		{"repo", "create", "acme/widget", "--private"},
		{"org", "team", "create", "acme", "core"},
		{"org", "team", "add", "acme", "core", "bob"},
		{"org", "team", "grant", "acme", "core", "acme/widget", "write"},
	} {
		if _, errOut, code := inst.ssh(t, aliceKey, "", args...); code != 0 {
			t.Fatalf("%v: %s", args, errOut)
		}
	}

	// bob pushes through the team grant.
	bobEnv := inst.gitEnv(bobKey)
	work := t.TempDir()
	mustGit(t, work, bobEnv, "clone", inst.sshURL("acme/widget"), "widget")
	dir := filepath.Join(work, "widget")
	if err := os.WriteFile(filepath.Join(dir, "README"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustGit(t, dir, bobEnv, "checkout", "-q", "-b", "main")
	mustGit(t, dir, bobEnv, "add", "README")
	mustGit(t, dir, bobEnv, "commit", "-q", "-m", "one")
	mustGit(t, dir, bobEnv, "push", "-q", "origin", "main")

	if _, errOut, code := inst.ssh(t, aliceKey, "", "org", "members", "remove", "acme", "bob"); code != 0 {
		t.Fatalf("members remove: %s", errOut)
	}
	out, _, _ := inst.ssh(t, aliceKey, "", "org", "team", "show", "acme", "core", "--json")
	if strings.Contains(out, `"bob"`) {
		t.Fatalf("team still lists the removed member: %s", out)
	}
	if err := os.WriteFile(filepath.Join(dir, "README"), []byte("again\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustGit(t, dir, bobEnv, "commit", "-q", "-am", "two")
	if out, code := gitRun(t, dir, bobEnv, "push", "-q", "origin", "main"); code == 0 {
		t.Fatalf("removed member still pushes:\n%s", out)
	}
	if _, _, code := inst.ssh(t, bobKey, "", "repo", "show", "acme/widget"); code != 3 {
		t.Fatalf("removed member still sees the private repo: exit %d", code)
	}

	// Re-adding to the org does not silently restore the team grant.
	if _, errOut, code := inst.ssh(t, aliceKey, "", "org", "members", "add", "acme", "bob"); code != 0 {
		t.Fatalf("members add: %s", errOut)
	}
	if _, _, code := inst.ssh(t, bobKey, "", "repo", "show", "acme/widget"); code != 3 {
		t.Fatalf("re-added member regained the team grant: exit %d", code)
	}
}
