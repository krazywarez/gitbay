package e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDiffComments(t *testing.T) {
	inst := startInstance(t)
	aliceKey := inst.newKey(t, "alice")
	bobKey := inst.newKey(t, "bob")
	eveKey := inst.newKey(t, "eve")
	inst.admin(t, "admin", "user", "create", "alice",
		"--key", aliceKey+".pub", "--email", "alice@example.test", "--verified")
	inst.admin(t, "admin", "user", "create", "bob", "--key", bobKey+".pub")
	inst.admin(t, "admin", "user", "create", "eve", "--key", eveKey+".pub")

	// MR with a real multi-line diff.
	if _, errOut, code := inst.ssh(t, aliceKey, "", "repo", "create", "alice/lib"); code != 0 {
		t.Fatalf("repo create: %s", errOut)
	}
	if _, _, code := inst.ssh(t, aliceKey, "", "repo", "access", "grant", "alice/lib", "bob", "write"); code != 0 {
		t.Fatal("grant failed")
	}
	work := t.TempDir()
	env := inst.gitEnv(aliceKey)
	mustGit(t, work, env, "clone", inst.sshURL("alice/lib"), "w")
	dir := filepath.Join(work, "w")
	os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n\nfunc main() {\n}\n"), 0o644)
	mustGit(t, dir, env, "checkout", "-q", "-b", "main")
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, env, "commit", "-q", "-m", "base")
	mustGit(t, dir, env, "push", "-q", "origin", "main")
	mustGit(t, dir, env, "checkout", "-q", "-b", "feat")
	os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n\nimport \"fmt\"\n\nfunc main() {\n\tfmt.Println(\"hi\")\n}\n"), 0o644)
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, env, "commit", "-q", "-m", "add greeting")
	mustGit(t, dir, env, "push", "-q", "origin", "feat")
	if _, errOut, code := inst.ssh(t, aliceKey, "", "mr", "create", "alice/lib",
		"--source", "feat", "--target", "main", "--title", "'greeting'"); code != 0 {
		t.Fatalf("mr create: %s", errOut)
	}

	// A thread on a real diff line; a path outside the diff is refused.
	if _, errOut, code := inst.ssh(t, bobKey, "", "mr", "diff-comment", "alice/lib", "1",
		"--path", "nope.go", "--line", "1", "--message", "'x'"); code != 2 || !strings.Contains(errOut, "not part of") {
		t.Fatalf("off-diff path: exit %d, %s", code, errOut)
	}
	out, errOut, code := inst.ssh(t, bobKey, "", "mr", "diff-comment", "alice/lib", "1",
		"--path", "main.go", "--line", "6", "--message", "'use log instead of fmt'", "--json")
	if code != 0 {
		t.Fatalf("diff-comment: %s", errOut)
	}
	var env2 struct {
		Data struct {
			Thread int64 `json:"thread"`
		} `json:"data"`
	}
	json.Unmarshal([]byte(out), &env2)
	thread := env2.Data.Thread

	// Reply joins the thread; replying to a reply is refused.
	out, _, code = inst.ssh(t, aliceKey, "", "mr", "diff-comment", "alice/lib", "1",
		"--reply", fmt.Sprint(thread), "--message", "'will do'", "--json")
	if code != 0 {
		t.Fatalf("reply failed: %s", out)
	}
	var env3 struct {
		Data struct {
			ID int64 `json:"id"`
		} `json:"data"`
	}
	json.Unmarshal([]byte(out), &env3)
	if _, errOut, code = inst.ssh(t, bobKey, "", "mr", "diff-comment", "alice/lib", "1",
		"--reply", fmt.Sprint(env3.Data.ID), "--message", "'nested'"); code != 2 || !strings.Contains(errOut, "thread root") {
		t.Fatalf("nested reply: exit %d, %s", code, errOut)
	}

	// Threads listing shows the thread, both comments, fresh.
	out, _, _ = inst.ssh(t, aliceKey, "", "mr", "threads", "alice/lib", "1", "--json")
	if !strings.Contains(out, `"path":"main.go"`) || !strings.Contains(out, `"line":6`) ||
		!strings.Contains(out, "will do") || strings.Contains(out, `"stale":true`) {
		t.Fatalf("threads: %s", out)
	}

	// mr show counts the unresolved thread; the web renders it inline.
	out, _, _ = inst.ssh(t, aliceKey, "", "mr", "show", "alice/lib", "1", "--json")
	if !strings.Contains(out, `"unresolved_threads":1`) {
		t.Fatalf("mr show count: %s", out)
	}
	status, body := inst.get(t, "/alice/lib/mrs/1?view=diff")
	if status != 200 || !strings.Contains(body, "use log instead of fmt") ||
		!strings.Contains(body, `class="thread`) {
		t.Fatalf("web thread: %d", status)
	}
	// Detached threads belong to the conversation, not the current diff.
	if _, conv := inst.get(t, "/alice/lib/mrs/1"); strings.Contains(conv, "Threads on earlier revisions") {
		t.Fatal("fresh thread rendered as detached")
	}

	// Resolution: eve (read-only outsider) cannot; the thread author can;
	// count drops; unresolve restores it.
	if _, _, code = inst.ssh(t, eveKey, "", "mr", "resolve", "alice/lib", "1", fmt.Sprint(thread)); code != 4 {
		t.Fatal("outsider resolved a thread")
	}
	if _, errOut, code = inst.ssh(t, bobKey, "", "mr", "resolve", "alice/lib", "1", fmt.Sprint(thread)); code != 0 {
		t.Fatalf("resolve: %s", errOut)
	}
	out, _, _ = inst.ssh(t, aliceKey, "", "mr", "show", "alice/lib", "1", "--json")
	if strings.Contains(out, `"unresolved_threads"`) {
		t.Fatalf("resolved thread still counted: %s", out)
	}
	if _, _, code = inst.ssh(t, bobKey, "", "mr", "unresolve", "alice/lib", "1", fmt.Sprint(thread)); code != 0 {
		t.Fatal("unresolve failed")
	}

	// Force-push moves the head: the thread goes stale and the web moves it
	// to the earlier-revisions section.
	mustGit(t, dir, env, "commit", "-q", "--amend", "-m", "add greeting (amended)")
	mustGit(t, dir, env, "push", "-q", "--force", "origin", "feat")
	out, _, _ = inst.ssh(t, aliceKey, "", "mr", "threads", "alice/lib", "1", "--json")
	if !strings.Contains(out, `"stale":true`) {
		t.Fatalf("thread not stale after force-push: %s", out)
	}
	_, body = inst.get(t, "/alice/lib/mrs/1")
	if !strings.Contains(body, "Threads on earlier revisions") {
		t.Fatal("stale thread not moved to detached section")
	}
}
