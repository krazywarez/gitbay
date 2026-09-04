package e2e

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestMRDraft covers the first stage of #111: a merge request opened to
// show work rather than to ask for a merge.
func TestMRDraft(t *testing.T) {
	inst := startInstanceWith(t, "[web]\nmode = \"accounts\"\n")
	aliceKey := inst.newKey(t, "alice")
	bobKey := inst.newKey(t, "bob")
	inst.admin(t, "admin", "user", "create", "alice", "--key", aliceKey+".pub")
	inst.admin(t, "admin", "user", "create", "bob", "--key", bobKey+".pub")
	if _, errOut, code := inst.ssh(t, aliceKey, "", "repo", "create", "alice/app"); code != 0 {
		t.Fatalf("repo create: %s", errOut)
	}
	if _, _, code := inst.ssh(t, aliceKey, "", "repo", "access", "grant", "alice/app", "bob", "write"); code != 0 {
		t.Fatal("grant failed")
	}

	env := inst.gitEnv(aliceKey)
	work := t.TempDir()
	mustGit(t, work, env, "clone", inst.sshURL("alice/app"), "w")
	dir := filepath.Join(work, "w")
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a\n"), 0o644)
	mustGit(t, dir, env, "checkout", "-q", "-b", "main")
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, env, "commit", "-q", "-m", "base")
	mustGit(t, dir, env, "push", "-q", "origin", "main")
	mustGit(t, dir, env, "checkout", "-q", "-b", "feat")
	mustGit(t, dir, env, "commit", "-q", "--allow-empty", "-m", "work")
	mustGit(t, dir, env, "push", "-q", "origin", "feat")

	if _, errOut, code := inst.ssh(t, aliceKey, "", "mr", "create", "alice/app",
		"--source", "feat", "--target", "main", "--title", "'wip'", "--draft"); code != 0 {
		t.Fatalf("mr create --draft: %s", errOut)
	}

	draft := func() bool {
		t.Helper()
		out, errOut, code := inst.ssh(t, aliceKey, "", "mr", "show", "alice/app", "1", "--json")
		if code != 0 {
			t.Fatalf("mr show: %s", errOut)
		}
		var env struct {
			Data struct {
				State string `json:"state"`
				Draft bool   `json:"draft"`
			} `json:"data"`
		}
		json.Unmarshal([]byte(out), &env)
		// A draft is open, not a fifth state.
		if env.Data.State != "open" {
			t.Fatalf("draft changed the state to %q", env.Data.State)
		}
		return env.Data.Draft
	}

	if !draft() {
		t.Fatal("--draft did not mark it")
	}

	// A draft does not merge, and no repository setting is involved.
	_, errOut, code := inst.ssh(t, aliceKey, "", "mr", "merge", "alice/app", "1")
	if code != 4 || !strings.Contains(errOut, "is a draft") {
		t.Fatalf("draft merged: exit %d, %s", code, errOut)
	}

	// Nor does it sit in anyone's review queue.
	out, _, _ := inst.ssh(t, bobKey, "", "dashboard", "--json")
	if strings.Contains(out, `"review_queue":[{`) {
		t.Fatalf("draft is in the review queue: %s", out)
	}

	// Ready, and now it does both.
	if _, errOut, code := inst.ssh(t, aliceKey, "", "mr", "ready", "alice/app", "1"); code != 0 {
		t.Fatalf("mr ready: %s", errOut)
	}
	if draft() {
		t.Fatal("mr ready did not clear the mark")
	}
	out, _, _ = inst.ssh(t, bobKey, "", "dashboard", "--json")
	if !strings.Contains(out, `"number":1`) {
		t.Fatalf("ready MR is not in the review queue: %s", out)
	}
	if _, errOut, code := inst.ssh(t, aliceKey, "", "mr", "merge", "alice/app", "1"); code != 0 {
		t.Fatalf("ready MR refused: %s", errOut)
	}

	// Ready twice is a usage error rather than a silent no-op.
	if _, _, code := inst.ssh(t, aliceKey, "", "mr", "ready", "alice/app", "1"); code == 0 {
		t.Fatal("mr ready on a merged MR succeeded")
	}
}
