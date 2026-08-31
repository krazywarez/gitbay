package e2e

import (
	"encoding/json"
	"strings"
	"testing"
)

type depsStatus struct {
	Data struct {
		Enabled     bool  `json:"enabled"`
		IssueNumber int64 `json:"issue_number"`
		Behind      []struct {
			Name string `json:"name"`
		} `json:"behind"`
	} `json:"data"`
}

// Dependency checking is opt-in per repository, and only its administrators
// can turn it on: the check tells a public registry what the repository
// depends on.
func TestDepsEnableDisable(t *testing.T) {
	inst := startInstance(t)
	aliceKey := inst.newKey(t, "alice")
	inst.admin(t, "admin", "user", "create", "alice", "--key", aliceKey+".pub",
		"--email", "alice@example.test", "--verified")
	bobKey := inst.newKey(t, "bob")
	inst.admin(t, "admin", "user", "create", "bob", "--key", bobKey+".pub",
		"--email", "bob@example.test", "--verified")

	if _, errOut, code := inst.ssh(t, aliceKey, "", "repo", "create", "alice/app"); code != 0 {
		t.Fatalf("repo create: %s", errOut)
	}

	status := func(key string) depsStatus {
		t.Helper()
		out, errOut, code := inst.ssh(t, key, "", "repo", "deps", "status", "alice/app", "--json")
		if code != 0 {
			t.Fatalf("deps status: %s", errOut)
		}
		var s depsStatus
		if err := json.Unmarshal([]byte(out), &s); err != nil {
			t.Fatalf("parsing status %q: %v", out, err)
		}
		return s
	}

	if status(aliceKey).Data.Enabled {
		t.Error("checks are on before being enabled")
	}

	// A reader can see the state but cannot change it.
	if _, errOut, code := inst.ssh(t, bobKey, "", "repo", "deps", "enable", "alice/app"); code == 0 {
		t.Error("a non-admin enabled dependency checks")
	} else if !strings.Contains(errOut, "denied") {
		t.Errorf("unexpected refusal: %s", errOut)
	}

	if _, errOut, code := inst.ssh(t, aliceKey, "", "repo", "deps", "enable", "alice/app"); code != 0 {
		t.Fatalf("deps enable: %s", errOut)
	}
	got := status(aliceKey)
	if !got.Data.Enabled {
		t.Error("checks are off after being enabled")
	}
	if len(got.Data.Behind) != 0 {
		t.Errorf("behind = %v before any sweep", got.Data.Behind)
	}

	// Enabling twice is not an error.
	if _, errOut, code := inst.ssh(t, aliceKey, "", "repo", "deps", "enable", "alice/app"); code != 0 {
		t.Fatalf("second deps enable: %s", errOut)
	}

	if _, errOut, code := inst.ssh(t, aliceKey, "", "repo", "deps", "disable", "alice/app"); code != 0 {
		t.Fatalf("deps disable: %s", errOut)
	}
	if status(aliceKey).Data.Enabled {
		t.Error("checks are on after being disabled")
	}
}
