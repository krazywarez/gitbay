package e2e

import (
	"strings"
	"testing"
)

// repo show carries the caller's own watch and bookmark state and, when
// the caller can read it, what the repository was forked from — so a
// client draws the real state without a second read per screen (#178).
func TestRepoShowCarriesViewerState(t *testing.T) {
	inst := startInstance(t)
	aliceKey := inst.newKey(t, "alice")
	bobKey := inst.newKey(t, "bob")
	inst.admin(t, "admin", "user", "create", "alice", "--key", aliceKey+".pub")
	inst.admin(t, "admin", "user", "create", "bob", "--key", bobKey+".pub")
	if _, errOut, code := inst.ssh(t, aliceKey, "", "repo", "create", "alice/app"); code != 0 {
		t.Fatalf("repo create: %s", errOut)
	}

	// Nothing yet: the fields are absent, not false or empty strings.
	out, _, _ := inst.ssh(t, bobKey, "", "repo", "show", "alice/app", "--json")
	for _, k := range []string{`"watch"`, `"bookmarked"`, `"fork_of"`} {
		if strings.Contains(out, k) {
			t.Errorf("%s present with no state:\n%s", k, out)
		}
	}

	inst.ssh(t, bobKey, "", "repo", "watch", "alice/app")
	inst.ssh(t, bobKey, "", "repo", "bookmark", "alice/app")
	out, _, _ = inst.ssh(t, bobKey, "", "repo", "show", "alice/app", "--json")
	if !strings.Contains(out, `"watch":"watching"`) || !strings.Contains(out, `"bookmarked":true`) {
		t.Fatalf("bob's state not reported:\n%s", out)
	}
	// It is the caller's state: alice sees her own, not bob's.
	if out, _, _ := inst.ssh(t, aliceKey, "", "repo", "show", "alice/app", "--json"); strings.Contains(out, `"bookmarked"`) {
		t.Errorf("alice sees bob's bookmark:\n%s", out)
	}

	// Plain output carries the same.
	if out, _, _ := inst.ssh(t, bobKey, "", "repo", "show", "alice/app"); !strings.Contains(out, "watch: watching") ||
		!strings.Contains(out, "bookmarked") {
		t.Errorf("plain output lacks the state:\n%s", out)
	}

	// A fork names its parent to anyone who can read the parent.
	if _, errOut, code := inst.ssh(t, bobKey, "", "repo", "fork", "alice/app"); code != 0 {
		t.Fatalf("fork: %s", errOut)
	}
	if out, _, _ := inst.ssh(t, bobKey, "", "repo", "show", "bob/app", "--json"); !strings.Contains(out, `"fork_of":"alice/app"`) {
		t.Fatalf("fork_of missing:\n%s", out)
	}
	// Once the parent is private and unreadable, the fork stops naming
	// it: a private repository is never confirmed to exist.
	inst.ssh(t, aliceKey, "", "repo", "settings", "visibility", "alice/app", "private")
	if out, _, _ := inst.ssh(t, bobKey, "", "repo", "show", "bob/app", "--json"); strings.Contains(out, "alice/app") {
		t.Errorf("a fork named a parent the caller cannot read:\n%s", out)
	}
}
