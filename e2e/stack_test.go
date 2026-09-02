package e2e

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A three-deep stack, merged bottom-up. Each merge moves the stack above
// it onto the merged target with its reviews intact; a squash under a
// stack is refused.
func TestStackedMergeRequests(t *testing.T) {
	inst := startInstanceWith(t, "[web]\nmode = \"accounts\"\n")
	aliceKey := inst.newKey(t, "alice")
	bobKey := inst.newKey(t, "bob")
	inst.admin(t, "admin", "user", "create", "alice", "--key", aliceKey+".pub", "--email", "alice@example.test", "--verified")
	inst.admin(t, "admin", "user", "create", "bob", "--key", bobKey+".pub", "--email", "bob@example.test", "--verified")
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
	commit := func(branch, from, file string) {
		t.Helper()
		if from == "" {
			mustGit(t, dir, env, "checkout", "-q", "-b", branch)
		} else {
			mustGit(t, dir, env, "checkout", "-q", "-b", branch, from)
		}
		os.WriteFile(filepath.Join(dir, file), []byte(file+"\n"), 0o644)
		mustGit(t, dir, env, "add", ".")
		mustGit(t, dir, env, "commit", "-q", "-m", file)
		mustGit(t, dir, env, "push", "-q", "origin", branch)
	}
	commit("main", "", "base.txt")
	commit("feat-a", "main", "a.txt")
	commit("feat-b", "feat-a", "b.txt")
	commit("feat-c", "feat-b", "c.txt")

	create := func(src, dst, title string) string {
		t.Helper()
		out, errOut, code := inst.ssh(t, aliceKey, "", "mr", "create", "alice/lib", "--source", src, "--target", dst, "--title", title, "--json")
		if code != 0 {
			t.Fatalf("mr create %s: %s", src, errOut)
		}
		return out
	}
	if out := create("feat-a", "main", "A"); strings.Contains(out, "stacked_on") {
		t.Fatalf("A is not stacked:\n%s", out)
	}
	if out := create("feat-b", "feat-a", "B"); !strings.Contains(out, `"stacked_on":{"number":1`) {
		t.Fatalf("B create does not report its stack:\n%s", out)
	}
	create("feat-c", "feat-b", "C")

	type stackShow struct {
		mrShow
		StackedOn *struct {
			Number int64 `json:"number"`
		} `json:"stacked_on"`
		Stacked []struct {
			Number int64 `json:"number"`
		} `json:"stacked"`
		Comments []struct {
			Body string `json:"body"`
		} `json:"comments"`
	}
	show := func(n string) stackShow {
		t.Helper()
		out, errOut, code := inst.ssh(t, aliceKey, "", "mr", "show", "alice/lib", n, "--json")
		if code != 0 {
			t.Fatalf("mr show %s: %s", n, errOut)
		}
		var env struct {
			Data stackShow `json:"data"`
		}
		if err := json.Unmarshal([]byte(out), &env); err != nil {
			t.Fatalf("show: %v\n%s", err, out)
		}
		return env.Data
	}
	a, b, c := show("1"), show("2"), show("3")
	if a.StackedOn != nil || len(a.Stacked) != 1 || a.Stacked[0].Number != 2 {
		t.Fatalf("A stack: %+v", a)
	}
	if b.StackedOn == nil || b.StackedOn.Number != 1 || len(b.Stacked) != 1 || b.Stacked[0].Number != 3 {
		t.Fatalf("B stack: %+v", b)
	}
	if c.StackedOn == nil || c.StackedOn.Number != 2 || len(c.Stacked) != 0 {
		t.Fatalf("C stack: %+v", c)
	}
	if out, _, _ := inst.ssh(t, aliceKey, "", "mr", "list", "alice/lib"); !strings.Contains(out, "stacked on !1") || !strings.Contains(out, "stacked on !2") {
		t.Fatalf("mr list lacks the stack:\n%s", out)
	}

	// Bob approves B; the approval must survive the retarget.
	if _, errOut, code := inst.ssh(t, bobKey, "", "mr", "review", "alice/lib", "2", "--approve"); code != 0 {
		t.Fatalf("review: %s", errOut)
	}

	// A squash under the stack is refused, naming the stack.
	if _, errOut, code := inst.ssh(t, aliceKey, "", "mr", "merge", "alice/lib", "1", "--strategy", "squash"); code != 2 || !strings.Contains(errOut, "!2") {
		t.Fatalf("squash under a stack: exit %d %s", code, errOut)
	}
	// Merge A: B moves onto main, C stays on feat-b.
	if _, errOut, code := inst.ssh(t, aliceKey, "", "mr", "merge", "alice/lib", "1"); code != 0 {
		t.Fatalf("merge A: %s", errOut)
	}
	b = show("2")
	if b.TargetRef != "main" || b.StackedOn != nil || len(b.Reviews) != 1 || b.Reviews[0].Stale {
		t.Fatalf("B after A merged: %+v", b)
	}
	found := false
	for _, cm := range b.Comments {
		if strings.Contains(cm.Body, "retargeted from feat-a to main: !1 merged") {
			found = true
		}
	}
	if !found {
		t.Fatalf("B carries no retarget comment: %+v", b.Comments)
	}
	if c = show("3"); c.TargetRef != "feat-b" || c.StackedOn == nil || c.StackedOn.Number != 2 {
		t.Fatalf("C after A merged: %+v", c)
	}
	// The web page says both directions.
	root := inst.login(t, aliceKey)
	if _, body := browserGet(t, root, inst.base()+"/alice/lib/mrs/2"); !strings.Contains(body, "Builds on this") || !strings.Contains(body, "/alice/lib/mrs/3") {
		t.Fatalf("B page lacks its stack:\n%s", body)
	}
	if _, body := browserGet(t, root, inst.base()+"/alice/lib/mrs/3"); !strings.Contains(body, "Stacked on") || !strings.Contains(body, "/alice/lib/mrs/2") {
		t.Fatalf("C page lacks its parent:\n%s", body)
	}
	// Merge B, then C: each lands its own commit only.
	if _, errOut, code := inst.ssh(t, aliceKey, "", "mr", "merge", "alice/lib", "2"); code != 0 {
		t.Fatalf("merge B: %s", errOut)
	}
	if c = show("3"); c.TargetRef != "main" || c.StackedOn != nil {
		t.Fatalf("C after B merged: %+v", c)
	}
	if _, errOut, code := inst.ssh(t, aliceKey, "", "mr", "merge", "alice/lib", "3"); code != 0 {
		t.Fatalf("merge C: %s", errOut)
	}
	mustGit(t, dir, env, "checkout", "-q", "main")
	mustGit(t, dir, env, "pull", "-q", "origin", "main")
	for _, f := range []string{"a.txt", "b.txt", "c.txt"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Fatalf("%s missing from main after the stack merged", f)
		}
	}
}
