package e2e

import (
	"strings"
	"testing"
)

func TestOwnerProfiles(t *testing.T) {
	inst := startInstance(t)
	aliceKey := inst.newKey(t, "alice")
	bobKey := inst.newKey(t, "bob")
	inst.admin(t, "admin", "user", "create", "alice", "--key", aliceKey+".pub")
	inst.admin(t, "admin", "user", "create", "bob", "--key", bobKey+".pub")

	// Self-service user profile; website validated.
	if _, errOut, code := inst.ssh(t, aliceKey, "",
		"profile", "set", "--description", "'builds small tools'", "--website", "https://alice.example"); code != 0 {
		t.Fatalf("profile set: %s", errOut)
	}
	if _, _, code := inst.ssh(t, aliceKey, "", "profile", "set", "--website", "gopher://nope"); code != 2 {
		t.Fatal("bad website scheme accepted")
	}
	out, _, _ := inst.ssh(t, bobKey, "", "profile", "show", "alice", "--json")
	if !strings.Contains(out, `"description":"builds small tools"`) || !strings.Contains(out, `"website":"https://alice.example"`) {
		t.Fatalf("profile show: %s", out)
	}

	// Partial update leaves the other field untouched; empty clears.
	if _, _, code := inst.ssh(t, aliceKey, "", "profile", "set", "--description", "'tinkerer'"); code != 0 {
		t.Fatal("partial set failed")
	}
	out, _, _ = inst.ssh(t, aliceKey, "", "profile", "show", "--json")
	if !strings.Contains(out, "tinkerer") || !strings.Contains(out, "alice.example") {
		t.Fatalf("partial update clobbered website: %s", out)
	}
	if _, _, code := inst.ssh(t, aliceKey, "", "profile", "set", "--website", "''"); code != 0 {
		t.Fatal("clear failed")
	}
	out, _, _ = inst.ssh(t, aliceKey, "", "profile", "show", "--json")
	if strings.Contains(out, "alice.example") {
		t.Fatalf("website not cleared: %s", out)
	}

	// Org profile: admin sets, member cannot; no flags shows.
	if _, _, code := inst.ssh(t, aliceKey, "", "org", "create", "workshop"); code != 0 {
		t.Fatal("org create failed")
	}
	if _, _, code := inst.ssh(t, aliceKey, "", "org", "members", "add", "workshop", "bob"); code != 0 {
		t.Fatal("member add failed")
	}
	if _, errOut, code := inst.ssh(t, aliceKey, "",
		"org", "profile", "workshop", "--description", "'where things get made'", "--website", "https://workshop.example"); code != 0 {
		t.Fatalf("org profile set: %s", errOut)
	}
	if _, _, code := inst.ssh(t, bobKey, "", "org", "profile", "workshop", "--description", "hax"); code != 4 {
		t.Fatal("member set org profile")
	}
	out, _, _ = inst.ssh(t, bobKey, "", "org", "profile", "workshop")
	if !strings.Contains(out, "where things get made") {
		t.Fatalf("org profile show: %s", out)
	}

	// Owner pages render description and website link.
	status, body := inst.get(t, "/alice")
	if status != 200 || !strings.Contains(body, "tinkerer") {
		t.Fatalf("user page profile: %d", status)
	}
	status, body = inst.get(t, "/workshop")
	if status != 200 || !strings.Contains(body, "where things get made") ||
		!strings.Contains(body, `href="https://workshop.example"`) {
		t.Fatalf("org page profile: %d\n%s", status, body)
	}
}
