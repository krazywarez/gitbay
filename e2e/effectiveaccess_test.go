package e2e

import (
	"strings"
	"testing"
)

// repo access list reports effective access with its source, and an
// outsider gets one answer about an org: it exists and has members;
// its teams are not theirs to see (#200).
func TestEffectiveAccessAndOutsiders(t *testing.T) {
	inst := startInstance(t)
	keys := map[string]string{}
	for _, u := range []string{"alice", "bob", "carol", "dave", "eve"} {
		keys[u] = inst.newKey(t, u)
		inst.admin(t, "admin", "user", "create", u, "--key", keys[u]+".pub")
	}
	for _, args := range [][]string{
		{"org", "create", "acme"},
		{"org", "members", "add", "acme", "bob"},
		{"org", "members", "add", "acme", "carol"},
		{"org", "members", "add", "acme", "dave"},
		{"org", "settings", "members-role", "acme", "read"},
		{"repo", "create", "acme/core", "--private"},
		{"org", "team", "create", "acme", "core"},
		{"org", "team", "add", "acme", "core", "bob"},
		{"org", "team", "grant", "acme", "core", "acme/core", "write"},
		{"repo", "access", "grant", "acme/core", "carol", "admin"},
	} {
		if _, errOut, code := inst.ssh(t, keys["alice"], "", args...); code != 0 {
			t.Fatalf("%v: %s", args, errOut)
		}
	}
	out, _, code := inst.ssh(t, keys["alice"], "", "repo", "access", "list", "acme/core", "--json")
	if code != 0 {
		t.Fatalf("access list: %s", out)
	}
	for _, want := range []string{
		`{"user":"alice","role":"admin","source":"org admin"}`,
		`{"user":"bob","role":"write","source":"team core"}`,
		`{"user":"carol","role":"admin","source":"direct"}`,
		`{"user":"dave","role":"read","source":"org member"}`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("access list missing %s:\n%s", want, out)
		}
	}
	if strings.Contains(out, `"eve"`) {
		t.Errorf("outsider listed:\n%s", out)
	}

	// eve: the org and its members are public, the teams are not.
	if _, errOut, code := inst.ssh(t, keys["eve"], "", "org", "show", "acme"); code != 0 {
		t.Fatalf("org show for an outsider: %d %s", code, errOut)
	}
	if _, errOut, code := inst.ssh(t, keys["eve"], "", "org", "members", "list", "acme"); code != 0 {
		t.Fatalf("org members list for an outsider: %d %s", code, errOut)
	}
	if _, errOut, code := inst.ssh(t, keys["eve"], "", "org", "team", "list", "acme"); code != 4 {
		t.Fatalf("org team list for an outsider: %d %s", code, errOut)
	}
	if _, errOut, code := inst.ssh(t, keys["eve"], "", "org", "team", "show", "acme", "core"); code != 4 {
		t.Fatalf("org team show for an outsider: %d %s", code, errOut)
	}
	if _, _, code := inst.ssh(t, keys["eve"], "", "org", "team", "list", "nosuch"); code != 3 {
		t.Fatalf("missing org: %d", code)
	}
}
