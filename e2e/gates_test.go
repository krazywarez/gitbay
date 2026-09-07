package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// mr show reports the gates before the merge fails, mr merge names every
// unmet gate at once, and mr review says when a verdict is advisory
// (#199).
func TestMergeGatesVisible(t *testing.T) {
	inst := startInstance(t)
	aliceKey := inst.newKey(t, "alice")
	bobKey := inst.newKey(t, "bob")
	carolKey := inst.newKey(t, "carol")
	inst.admin(t, "admin", "user", "create", "alice", "--key", aliceKey+".pub")
	inst.admin(t, "admin", "user", "create", "bob", "--key", bobKey+".pub")
	inst.admin(t, "admin", "user", "create", "carol", "--key", carolKey+".pub")
	for _, args := range [][]string{
		{"repo", "create", "alice/svc"},
		{"repo", "access", "grant", "alice/svc", "bob", "write"},
		{"repo", "settings", "require-approvals", "alice/svc", "1"},
		{"repo", "settings", "require-codeowners", "alice/svc", "on"},
		{"repo", "settings", "require-resolved", "alice/svc", "on"},
	} {
		if _, errOut, code := inst.ssh(t, aliceKey, "", args...); code != 0 {
			t.Fatalf("%v: %s", args, errOut)
		}
	}
	env := inst.gitEnv(aliceKey)
	work := t.TempDir()
	mustGit(t, work, env, "clone", "-q", inst.sshURL("alice/svc"), "w")
	dir := filepath.Join(work, "w")
	mustGit(t, dir, env, "checkout", "-q", "-b", "main")
	os.WriteFile(filepath.Join(dir, "CODEOWNERS"), []byte("*.go @bob\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "svc.go"), []byte("package svc\n"), 0o644)
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, env, "commit", "-q", "-m", "base")
	mustGit(t, dir, env, "push", "-q", "origin", "main")
	mustGit(t, dir, env, "checkout", "-q", "-b", "feat")
	os.WriteFile(filepath.Join(dir, "svc.go"), []byte("package svc\n\nvar V = 1\n"), 0o644)
	mustGit(t, dir, env, "commit", "-q", "-am", "change")
	mustGit(t, dir, env, "push", "-q", "origin", "feat")
	if _, errOut, code := inst.ssh(t, aliceKey, "", "mr", "create", "alice/svc",
		"--source", "feat", "--target", "main", "--title", "'change'"); code != 0 {
		t.Fatalf("mr create: %s", errOut)
	}

	// Before anyone reviews: two unmet gates, visible on mr show.
	out, _, _ := inst.ssh(t, aliceKey, "", "mr", "show", "alice/svc", "1", "--json")
	for _, want := range []string{
		`"approvals_required":1`, `"codeowners_required":true`, `"resolved_required":true`,
		`"owners_outstanding":[{"files":["svc.go"],"owners":["bob"]}]`, `"fast_forward":true`,
		`requires 1 fresh approval(s)`, `CODEOWNERS approval missing for: svc.go (owned by bob)`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("mr show gates missing %s:\n%s", want, out)
		}
	}
	// The merge names both at once.
	if _, errOut, code := inst.ssh(t, aliceKey, "", "mr", "merge", "alice/svc", "1"); code != 4 ||
		!strings.Contains(errOut, "fresh approval") || !strings.Contains(errOut, "CODEOWNERS approval missing") {
		t.Fatalf("merge refusal does not name every gate: %d %s", code, errOut)
	}

	// A reader's approval is advisory, and says so when made.
	out, _, code := inst.ssh(t, carolKey, "", "mr", "review", "alice/svc", "1", "--approve", "--json")
	if code != 0 || !strings.Contains(out, `"counts":false`) {
		t.Fatalf("reader review: %d %s", code, out)
	}
	if out, _, _ := inst.ssh(t, carolKey, "", "mr", "review", "alice/svc", "1", "--comment"); !strings.Contains(out, "advisory") {
		t.Fatalf("reader review does not say it is advisory: %s", out)
	}
	out, _, _ = inst.ssh(t, aliceKey, "", "mr", "show", "alice/svc", "1", "--json")
	if !strings.Contains(out, `requires 1 fresh approval(s)`) {
		t.Fatalf("advisory approval counted in the gates:\n%s", out)
	}

	// The owner's approval meets both; the block says so and the merge lands.
	out, _, _ = inst.ssh(t, bobKey, "", "mr", "review", "alice/svc", "1", "--approve", "--json")
	if !strings.Contains(out, `"counts":true`) {
		t.Fatalf("writer review: %s", out)
	}
	out, _, _ = inst.ssh(t, aliceKey, "", "mr", "show", "alice/svc", "1", "--json")
	if strings.Contains(out, `"unmet"`) || !strings.Contains(out, `"approvals":["bob"]`) {
		t.Fatalf("gates after approval:\n%s", out)
	}
	if out, _, _ := inst.ssh(t, aliceKey, "", "mr", "show", "alice/svc", "1"); !strings.Contains(out, "gates: met; fast-forward possible") {
		t.Fatalf("text gates line: %s", out)
	}
	if _, errOut, code := inst.ssh(t, aliceKey, "", "mr", "merge", "alice/svc", "1"); code != 0 {
		t.Fatalf("merge: %s", errOut)
	}
	if out, _, _ := inst.ssh(t, aliceKey, "", "mr", "show", "alice/svc", "1", "--json"); strings.Contains(out, `"gates"`) {
		t.Fatalf("gates reported on a merged request:\n%s", out)
	}
}
