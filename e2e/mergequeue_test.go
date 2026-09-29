package e2e

import (
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A merge queued with mr merge --when-ready waits on its gates, shows on
// the page with a cancel button, and merges as the user who queued it
// when a status turns the checks green (#289).
func TestMergeWhenReady(t *testing.T) {
	t.Parallel()
	inst := startInstanceWith(t, "[web]\nmode = \"accounts\"\n")
	aliceKey := inst.newKey(t, "alice")
	bobKey := inst.newKey(t, "bob")
	inst.admin(t, "admin", "user", "create", "alice",
		"--key", aliceKey+".pub", "--email", "alice@example.test", "--verified")
	inst.admin(t, "admin", "user", "create", "bob",
		"--key", bobKey+".pub", "--email", "bob@example.test", "--verified")
	for _, args := range [][]string{
		{"repo", "create", "alice/lib"},
		{"repo", "access", "grant", "alice/lib", "bob", "write"},
		{"repo", "settings", "require-contexts", "alice/lib", "ext/test"},
	} {
		if _, errOut, code := inst.ssh(t, aliceKey, "", args...); code != 0 {
			t.Fatalf("%v: %s", args, errOut)
		}
	}

	env := inst.gitEnv(aliceKey)
	work := t.TempDir()
	mustGit(t, work, env, "clone", "-q", inst.sshURL("alice/lib"), "w")
	dir := filepath.Join(work, "w")
	mustGit(t, dir, env, "checkout", "-q", "-b", "main")
	os.WriteFile(filepath.Join(dir, "lib.txt"), []byte("v1\n"), 0o644)
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, env, "commit", "-q", "-m", "base")
	mustGit(t, dir, env, "push", "-q", "origin", "main")
	mustGit(t, dir, env, "checkout", "-q", "-b", "feature")
	os.WriteFile(filepath.Join(dir, "lib.txt"), []byte("v2\n"), 0o644)
	mustGit(t, dir, env, "commit", "-q", "-am", "change")
	mustGit(t, dir, env, "push", "-q", "origin", "feature")
	head := strings.TrimSpace(mustGit(t, dir, env, "rev-parse", "HEAD"))
	if _, errOut, code := inst.ssh(t, aliceKey, "", "mr", "create", "alice/lib",
		"--source", "feature", "--target", "main", "--title", "'change'"); code != 0 {
		t.Fatalf("mr create: %s", errOut)
	}

	// Queued over SSH: the checks have not reported, so it waits.
	out, errOut, code := inst.ssh(t, bobKey, "", "mr", "merge", "alice/lib", "1", "--when-ready")
	if code != 0 || !strings.Contains(out, "queued") || !strings.Contains(out, "ext/test=missing") {
		t.Fatalf("mr merge --when-ready: %d %s %s", code, out, errOut)
	}
	out, _, _ = inst.ssh(t, aliceKey, "", "mr", "show", "alice/lib", "1", "--json")
	if !strings.Contains(out, `"queued":{"by":"bob"`) {
		t.Fatalf("mr show does not carry the queue:\n%s", out)
	}
	out, _, _ = inst.ssh(t, bobKey, "", "dashboard", "--json")
	if !strings.Contains(out, `"queued":true`) {
		t.Fatalf("dashboard does not carry the queue:\n%s", out)
	}

	// The page shows it; its cancel button dequeues and its queue button
	// queues again, now as alice.
	mrURL := inst.base() + "/alice/lib/mrs/1"
	alice := inst.login(t, aliceKey)
	_, body := browserGet(t, alice, mrURL)
	for _, want := range []string{"Queued to merge by", "waiting: ", "Cancel queued merge"} {
		if !strings.Contains(body, want) {
			t.Fatalf("MR page missing %q:\n%s", want, body)
		}
	}
	if status, _ := browserPost(t, alice, mrURL+"/merge", url.Values{"cancel": {"on"}}); status != 200 {
		t.Fatalf("cancel post: %d", status)
	}
	if out, _, _ := inst.ssh(t, aliceKey, "", "mr", "show", "alice/lib", "1", "--json"); strings.Contains(out, `"queued":`) {
		t.Fatalf("web cancel left the queue:\n%s", out)
	}
	if status, _ := browserPost(t, alice, mrURL+"/merge",
		url.Values{"strategy": {"auto"}, "when_ready": {"on"}}); status != 200 {
		t.Fatalf("queue post: %d", status)
	}
	if st := inst.mrShow(t, aliceKey, "alice/lib", "1").State; st != "open" {
		t.Fatalf("queued MR is %s before its checks", st)
	}

	// The status that turns the checks green merges it, as alice.
	if _, errOut, code := inst.ssh(t, bobKey, "", "status", "set", "alice/lib", head,
		"--context", "ext/test", "--state", "success"); code != 0 {
		t.Fatalf("status set: %s", errOut)
	}
	merged := inst.mrShow(t, aliceKey, "alice/lib", "1")
	if merged.State != "merged" || merged.MergedBy != "alice" {
		t.Fatalf("after green checks: state %s merged by %q", merged.State, merged.MergedBy)
	}
	mustGit(t, dir, env, "fetch", "-q", "origin")
	if got := strings.TrimSpace(mustGit(t, dir, env, "rev-parse", "origin/main")); got != head {
		t.Fatalf("main = %s, want %s", got, head)
	}
}
