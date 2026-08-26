package e2e

import (
	"strings"
	"testing"
)

// TestBuildBadge covers the badge endpoint: it reports the newest build,
// says "unknown" before any build exists, and 404s for a private repo so
// it cannot be used to probe for one.
func TestBuildBadge(t *testing.T) {
	inst := startInstance(t)
	aliceKey := inst.newKey(t, "alice")
	inst.admin(t, "admin", "user", "create", "alice", "--key", aliceKey+".pub")
	if _, errOut, code := inst.ssh(t, aliceKey, "", "repo", "create", "alice/app"); code != 0 {
		t.Fatalf("repo create: %s", errOut)
	}

	status, body := inst.get(t, "/alice/app/badge/build.svg")
	if status != 200 || !strings.Contains(body, "unknown") || !strings.Contains(body, "<svg") {
		t.Fatalf("badge before any build: %d\n%s", status, body)
	}

	// Private repositories have no badge, like every other surface.
	if _, _, code := inst.ssh(t, aliceKey, "", "repo", "create", "alice/vault", "--private"); code != 0 {
		t.Fatal("private repo create failed")
	}
	if status, _ := inst.get(t, "/alice/vault/badge/build.svg"); status != 404 {
		t.Fatalf("private badge: %d", status)
	}
	if status, _ := inst.get(t, "/alice/nope/badge/build.svg"); status != 404 {
		t.Fatalf("missing repo badge: %d", status)
	}
}
