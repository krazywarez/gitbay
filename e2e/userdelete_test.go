package e2e

import (
	"strings"
	"testing"
)

func TestAdminUserDelete(t *testing.T) {
	inst := startInstance(t)
	aliceKey := inst.newKey(t, "alice")
	inst.admin(t, "admin", "user", "create", "alice", "--key", aliceKey+".pub")

	// --yes is required.
	out := inst.forgedAdminErr(t, "admin", "user", "delete", "alice")
	if !strings.Contains(out, "--yes") {
		t.Fatalf("missing confirmation guard: %s", out)
	}

	// An owned repo blocks deletion, by name of the blocker.
	if _, errOut, code := inst.ssh(t, aliceKey, "", "repo", "create", "alice/app"); code != 0 {
		t.Fatalf("repo create: %s", errOut)
	}
	out = inst.forgedAdminErr(t, "admin", "user", "delete", "alice", "--yes")
	if !strings.Contains(out, "owned repositories") {
		t.Fatalf("repo blocker not reported: %s", out)
	}

	// Authored content blocks deletion after the repo is gone... the issue
	// lives in bob's repo so it survives alice/app's deletion.
	bobKey := inst.newKey(t, "bob")
	inst.admin(t, "admin", "user", "create", "bob", "--key", bobKey+".pub")
	if _, errOut, code := inst.ssh(t, bobKey, "", "repo", "create", "bob/proj"); code != 0 {
		t.Fatalf("bob repo: %s", errOut)
	}
	if _, errOut, code := inst.ssh(t, aliceKey, "", "issue", "create", "bob/proj", "--title", "'from alice'"); code != 0 {
		t.Fatalf("issue create: %s", errOut)
	}
	if _, _, code := inst.ssh(t, aliceKey, "", "repo", "delete", "alice/app", "--yes"); code != 0 {
		t.Fatal("repo delete failed")
	}
	out = inst.forgedAdminErr(t, "admin", "user", "delete", "alice", "--yes")
	if !strings.Contains(out, "authored issues") {
		t.Fatalf("issue blocker not reported: %s", out)
	}

	// A clean account deletes; its key stops authenticating.
	carolKey := inst.newKey(t, "carol")
	inst.admin(t, "admin", "user", "create", "carol", "--key", carolKey+".pub")
	if out := inst.admin(t, "admin", "user", "delete", "carol", "--yes"); !strings.Contains(out, "deleted carol") {
		t.Fatalf("delete: %s", out)
	}
	if _, _, code := inst.ssh(t, carolKey, "", "whoami"); code == 0 {
		t.Fatal("deleted account still authenticates")
	}
	out = inst.forgedAdminErr(t, "admin", "user", "delete", "carol", "--yes")
	if !strings.Contains(out, "no user") {
		t.Fatalf("second delete: %s", out)
	}

	// The sole admin of an org is anchored by it.
	if _, errOut, code := inst.ssh(t, bobKey, "", "org", "create", "solo"); code != 0 {
		t.Fatalf("org create: %s", errOut)
	}
	out = inst.forgedAdminErr(t, "admin", "user", "delete", "bob", "--yes")
	if !strings.Contains(out, "no other admin") {
		t.Fatalf("org blocker not reported: %s", out)
	}
}
