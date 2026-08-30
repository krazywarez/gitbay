package e2e

import (
	"strings"
	"testing"
)

// The dashboard reports which build is serving, so the running commit is
// visible without reading the journal. It is admin-only: the exact build a
// host runs narrows down which known issues apply to it.
func TestDashboardReportsTheServerBuild(t *testing.T) {
	inst := startInstance(t)

	adminKey := inst.newKey(t, "root")
	userKey := inst.newKey(t, "plain")
	inst.admin(t, "admin", "user", "create", "root", "--key", adminKey+".pub", "--admin")
	inst.admin(t, "admin", "user", "create", "plain", "--key", userKey+".pub")

	out, errOut, code := inst.ssh(t, adminKey, "", "dashboard", "--json")
	if code != 0 {
		t.Fatalf("admin dashboard: %s", errOut)
	}
	if !strings.Contains(out, `"server":{"commit":"`) {
		t.Fatalf("admin dashboard did not report the build:\n%s", out)
	}

	// A non-admin gets the same dashboard without it. omitempty drops the key
	// entirely rather than reporting an empty string.
	out, errOut, code = inst.ssh(t, userKey, "", "dashboard", "--json")
	if code != 0 {
		t.Fatalf("user dashboard: %s", errOut)
	}
	if strings.Contains(out, `"server"`) {
		t.Fatalf("a non-admin was told the server build:\n%s", out)
	}
	if !strings.Contains(out, `"review_queue"`) {
		t.Fatalf("user dashboard is missing its usual contents:\n%s", out)
	}

	// Human output carries it too, for the operator who did not ask for JSON.
	out, errOut, code = inst.ssh(t, adminKey, "", "dashboard")
	if code != 0 {
		t.Fatalf("admin dashboard (human): %s", errOut)
	}
	if !strings.Contains(out, "server:") || !strings.Contains(out, "build ") {
		t.Fatalf("human dashboard did not report the build:\n%s", out)
	}
}
