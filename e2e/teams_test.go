package e2e

import (
	"strings"
	"testing"
)

func TestOrgTeams(t *testing.T) {
	inst := startInstance(t)
	adminKey := inst.newKey(t, "alice")
	bobKey := inst.newKey(t, "bob")
	carolKey := inst.newKey(t, "carol")
	eveKey := inst.newKey(t, "eve")
	inst.admin(t, "admin", "user", "create", "alice", "--key", adminKey+".pub")
	inst.admin(t, "admin", "user", "create", "bob", "--key", bobKey+".pub")
	inst.admin(t, "admin", "user", "create", "carol", "--key", carolKey+".pub")
	inst.admin(t, "admin", "user", "create", "eve", "--key", eveKey+".pub")

	// Org with two private repos; bob and carol are plain members.
	for _, args := range [][]string{
		{"org", "create", "acme"},
		{"org", "members", "add", "acme", "bob"},
		{"org", "members", "add", "acme", "carol"},
		{"repo", "create", "acme/core", "--private"},
		{"repo", "create", "acme/site", "--private"},
	} {
		if _, errOut, code := inst.ssh(t, adminKey, "", args...); code != 0 {
			t.Fatalf("%v: %s", args, errOut)
		}
	}

	// Degenerate case: plain membership implies write everywhere.
	if _, _, code := inst.ssh(t, bobKey, "", "issue", "create", "acme/core", "--title", "'pre'"); code != 0 {
		t.Fatal("member write lost (degenerate case broken)")
	}

	// Scope the org: members get nothing by default, teams grant.
	if _, _, code := inst.ssh(t, bobKey, "", "org", "settings", "members-role", "acme", "none"); code != 4 {
		t.Fatal("non-admin changed members-role")
	}
	if _, _, code := inst.ssh(t, adminKey, "", "org", "settings", "members-role", "acme", "none"); code != 0 {
		t.Fatal("members-role failed")
	}
	// bob now cannot even see the private repo.
	if _, _, code := inst.ssh(t, bobKey, "", "repo", "show", "acme/core"); code != 3 {
		t.Fatal("scoped member still sees private repo")
	}

	// Team "core-devs": bob gets write on core, read on site.
	if _, _, code := inst.ssh(t, adminKey, "", "org", "team", "create", "acme", "core-devs"); code != 0 {
		t.Fatal("team create failed")
	}
	if _, errOut, code := inst.ssh(t, adminKey, "", "org", "team", "add", "acme", "core-devs", "eve"); code != 2 || !strings.Contains(errOut, "not a member") {
		t.Fatalf("non-member added to team: %d %s", code, errOut)
	}
	if _, _, code := inst.ssh(t, adminKey, "", "org", "team", "add", "acme", "core-devs", "bob"); code != 0 {
		t.Fatal("team add failed")
	}
	if _, _, code := inst.ssh(t, adminKey, "", "org", "team", "grant", "acme", "core-devs", "acme/core", "write"); code != 0 {
		t.Fatal("team grant failed")
	}
	if _, _, code := inst.ssh(t, adminKey, "", "org", "team", "grant", "acme", "core-devs", "acme/site", "read"); code != 0 {
		t.Fatal("second grant failed")
	}
	// Grants are limited to the org's own repos.
	if _, _, code := inst.ssh(t, adminKey, "", "repo", "create", "alice/own"); code != 0 {
		t.Fatal("repo create failed")
	}
	if _, errOut, code := inst.ssh(t, adminKey, "", "org", "team", "grant", "acme", "core-devs", "alice/own", "read"); code != 2 || !strings.Contains(errOut, "own org") {
		t.Fatalf("cross-org grant allowed: %d %s", code, errOut)
	}

	// bob: write on core (can open issues), read-only on site (visible,
	// not writable). carol (no team): nothing.
	if _, _, code := inst.ssh(t, bobKey, "", "issue", "create", "acme/core", "--title", "'works'"); code != 0 {
		t.Fatal("team write not effective")
	}
	if _, _, code := inst.ssh(t, bobKey, "", "repo", "show", "acme/site"); code != 0 {
		t.Fatal("team read not effective")
	}
	if _, _, code := inst.ssh(t, bobKey, "", "repo", "settings", "protect", "acme/site", "main"); code != 4 {
		t.Fatal("read grant allowed admin action")
	}
	if _, _, code := inst.ssh(t, carolKey, "", "repo", "show", "acme/core"); code != 3 {
		t.Fatal("teamless member sees scoped repo")
	}
	out, _, _ := inst.ssh(t, bobKey, "", "repo", "list")
	if !strings.Contains(out, "acme/core") || !strings.Contains(out, "acme/site") {
		t.Fatalf("team repos missing from listing: %s", out)
	}

	// show reflects members and grants; member removal drops access.
	out, _, _ = inst.ssh(t, adminKey, "", "org", "team", "show", "acme", "core-devs", "--json")
	if !strings.Contains(out, `"members":["bob"]`) || !strings.Contains(out, `"repo":"acme/core","role":"write"`) {
		t.Fatalf("team show: %s", out)
	}
	if _, _, code := inst.ssh(t, adminKey, "", "org", "team", "remove", "acme", "core-devs", "bob"); code != 0 {
		t.Fatal("team remove failed")
	}
	if _, _, code := inst.ssh(t, bobKey, "", "repo", "show", "acme/core"); code != 3 {
		t.Fatal("removed member kept access")
	}

	// Deleting the team cascades its grants.
	inst.ssh(t, adminKey, "", "org", "team", "add", "acme", "core-devs", "carol")
	if _, _, code := inst.ssh(t, carolKey, "", "repo", "show", "acme/core"); code != 0 {
		t.Fatal("carol team access missing")
	}
	if _, _, code := inst.ssh(t, adminKey, "", "org", "team", "delete", "acme", "core-devs"); code != 0 {
		t.Fatal("team delete failed")
	}
	if _, _, code := inst.ssh(t, carolKey, "", "repo", "show", "acme/core"); code != 3 {
		t.Fatal("deleted team's grant survived")
	}
	// Org admins keep admin regardless of scoping.
	if _, _, code := inst.ssh(t, adminKey, "", "repo", "settings", "protect", "acme/core", "main"); code != 0 {
		t.Fatal("org admin lost access")
	}
}
