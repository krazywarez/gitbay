package e2e

import (
	"strings"
	"testing"
)

func TestRepoDescriptions(t *testing.T) {
	inst := startInstance(t)
	aliceKey := inst.newKey(t, "alice")
	inst.admin(t, "admin", "user", "create", "alice", "--key", aliceKey+".pub")

	// Set at creation; visible in show and list (human and JSON).
	if _, errOut, code := inst.ssh(t, aliceKey, "",
		"repo", "create", "alice/tool", "--description", "'a fine tool'"); code != 0 {
		t.Fatalf("create: %s", errOut)
	}
	out, _, _ := inst.ssh(t, aliceKey, "", "repo", "show", "alice/tool", "--json")
	if !strings.Contains(out, `"description":"a fine tool"`) {
		t.Fatalf("show json: %s", out)
	}
	out, _, _ = inst.ssh(t, aliceKey, "", "repo", "list")
	if !strings.Contains(out, "a fine tool") {
		t.Fatalf("list: %s", out)
	}

	// The description lives in the bare repo's native description file.
	sshOut, _, _ := inst.ssh(t, aliceKey, "", "repo", "show", "alice/tool")
	if !strings.Contains(sshOut, "a fine tool") {
		t.Fatalf("show human: %s", sshOut)
	}

	// Update via settings; first line only, trimmed.
	if _, errOut, code := inst.ssh(t, aliceKey, "",
		"repo", "settings", "description", "alice/tool", "'better now'"); code != 0 {
		t.Fatalf("settings description: %s", errOut)
	}
	out, _, _ = inst.ssh(t, aliceKey, "", "repo", "show", "alice/tool", "--json")
	if !strings.Contains(out, `"description":"better now"`) {
		t.Fatalf("updated show: %s", out)
	}

	// Non-admins cannot set it.
	bobKey := inst.newKey(t, "bob")
	inst.admin(t, "admin", "user", "create", "bob", "--key", bobKey+".pub")
	if _, _, code := inst.ssh(t, bobKey, "", "repo", "settings", "description", "alice/tool", "hax"); code != 4 {
		t.Fatalf("non-admin set: exit %d, want 4", code)
	}

	// Web: index, owner page, and repo header all show it.
	for _, path := range []string{"/", "/alice", "/alice/tool"} {
		status, body := inst.get(t, path)
		if status != 200 || !strings.Contains(body, "better now") {
			t.Fatalf("description missing at %s (%d)", path, status)
		}
	}

	// Forks inherit the description.
	if _, errOut, code := inst.ssh(t, bobKey, "", "repo", "fork", "alice/tool"); code != 0 {
		t.Fatalf("fork: %s", errOut)
	}
	out, _, _ = inst.ssh(t, bobKey, "", "repo", "show", "bob/tool", "--json")
	if !strings.Contains(out, `"description":"better now"`) {
		t.Fatalf("fork description: %s", out)
	}

	// A repo with no description set stays clean (git's placeholder is
	// treated as empty).
	if _, _, code := inst.ssh(t, aliceKey, "", "repo", "create", "alice/plain"); code != 0 {
		t.Fatal("plain create failed")
	}
	out, _, _ = inst.ssh(t, aliceKey, "", "repo", "show", "alice/plain", "--json")
	if strings.Contains(out, "Unnamed repository") || strings.Contains(out, `"description"`) {
		t.Fatalf("placeholder leaked: %s", out)
	}
}
