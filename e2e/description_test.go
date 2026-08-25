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

	// Web: explore, owner page, and repo header all show it.
	for _, path := range []string{"/explore", "/alice", "/alice/tool"} {
		status, body := inst.get(t, path)
		if status != 200 || !strings.Contains(body, "better now") {
			t.Fatalf("description missing at %s (%d)", path, status)
		}
	}

	// Website: set, shown in show and on the repo page; bad scheme and
	// non-admins refused.
	if _, errOut, code := inst.ssh(t, aliceKey, "",
		"repo", "settings", "website", "alice/tool", "https://tool.example.org"); code != 0 {
		t.Fatalf("settings website: %s", errOut)
	}
	out, _, _ = inst.ssh(t, aliceKey, "", "repo", "show", "alice/tool", "--json")
	if !strings.Contains(out, `"website":"https://tool.example.org"`) {
		t.Fatalf("website show: %s", out)
	}
	if _, body := inst.get(t, "/alice/tool"); !strings.Contains(body, `href="https://tool.example.org"`) {
		t.Fatalf("website missing on repo page:\n%s", body)
	}
	if _, _, code := inst.ssh(t, aliceKey, "", "repo", "settings", "website", "alice/tool", "javascript:alert(1)"); code != 2 {
		t.Fatalf("bad scheme accepted")
	}
	if _, _, code := inst.ssh(t, bobKey, "", "repo", "settings", "website", "alice/tool", "https://x.example"); code != 4 {
		t.Fatalf("non-admin set website: want exit 4")
	}
	// Clearing removes it.
	if _, _, code := inst.ssh(t, aliceKey, "", "repo", "settings", "website", "alice/tool", "''"); code != 0 {
		t.Fatal("website clear failed")
	}
	out, _, _ = inst.ssh(t, aliceKey, "", "repo", "show", "alice/tool", "--json")
	if strings.Contains(out, `"website"`) {
		t.Fatalf("website not cleared: %s", out)
	}
	if _, _, code := inst.ssh(t, aliceKey, "",
		"repo", "settings", "website", "alice/tool", "https://tool.example.org"); code != 0 {
		t.Fatal("website re-set failed")
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
