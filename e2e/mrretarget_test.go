package e2e

import (
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestMRRetarget moves an open merge request onto another branch, over
// SSH and from the browser. Retargeting changes which diff a review was
// of, so the existing approvals have to go stale with it.
func TestMRRetarget(t *testing.T) {
	inst := startInstanceWith(t, "[web]\nmode = \"accounts\"\n")
	aliceKey := inst.newKey(t, "alice")
	bobKey := inst.newKey(t, "bob")
	inst.admin(t, "admin", "user", "create", "alice",
		"--key", aliceKey+".pub", "--email", "alice@example.test", "--verified")
	inst.admin(t, "admin", "user", "create", "bob",
		"--key", bobKey+".pub", "--email", "bob@example.test", "--verified")

	if _, errOut, code := inst.ssh(t, aliceKey, "", "repo", "create", "alice/lib"); code != 0 {
		t.Fatalf("repo create: %s", errOut)
	}
	if _, errOut, code := inst.ssh(t, aliceKey, "", "repo", "access", "grant", "alice/lib", "bob", "write"); code != 0 {
		t.Fatalf("grant: %s", errOut)
	}

	env := inst.gitEnv(aliceKey)
	work := t.TempDir()
	mustGit(t, work, env, "clone", inst.sshURL("alice/lib"), "w")
	dir := filepath.Join(work, "w")
	os.WriteFile(filepath.Join(dir, "lib.txt"), []byte("v1\n"), 0o644)
	mustGit(t, dir, env, "checkout", "-q", "-b", "main")
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, env, "commit", "-q", "-m", "base")
	mustGit(t, dir, env, "push", "-q", "origin", "main")
	// A second long-lived branch to retarget onto.
	mustGit(t, dir, env, "checkout", "-q", "-b", "release")
	os.WriteFile(filepath.Join(dir, "release.txt"), []byte("1.0\n"), 0o644)
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, env, "commit", "-q", "-m", "cut release")
	mustGit(t, dir, env, "push", "-q", "origin", "release")
	mustGit(t, dir, env, "checkout", "-q", "-b", "feat", "main")
	os.WriteFile(filepath.Join(dir, "feat.txt"), []byte("work\n"), 0o644)
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, env, "commit", "-q", "-m", "add feat")
	mustGit(t, dir, env, "push", "-q", "origin", "feat")

	if _, errOut, code := inst.ssh(t, aliceKey, "", "mr", "create", "alice/lib",
		"--source", "feat", "--target", "main", "--title", "'feature'"); code != 0 {
		t.Fatalf("mr create: %s", errOut)
	}
	if _, errOut, code := inst.ssh(t, bobKey, "", "mr", "review", "alice/lib", "1", "--approve"); code != 0 {
		t.Fatalf("review: %s", errOut)
	}

	// Refusals first: an unknown branch, the branch it already targets,
	// and its own source.
	if _, errOut, code := inst.ssh(t, aliceKey, "", "mr", "retarget", "alice/lib", "1", "nope"); code != 3 ||
		!strings.Contains(errOut, "not found") {
		t.Fatalf("unknown branch: exit %d, %s", code, errOut)
	}
	if _, errOut, code := inst.ssh(t, aliceKey, "", "mr", "retarget", "alice/lib", "1", "main"); code != 2 ||
		!strings.Contains(errOut, "already targets") {
		t.Fatalf("same branch: exit %d, %s", code, errOut)
	}
	if _, errOut, code := inst.ssh(t, aliceKey, "", "mr", "retarget", "alice/lib", "1", "feat"); code != 2 ||
		!strings.Contains(errOut, "source branch") {
		t.Fatalf("source branch: exit %d, %s", code, errOut)
	}
	// Nobody outside the repo can move it.
	eveKey := inst.newKey(t, "eve")
	inst.admin(t, "admin", "user", "create", "eve", "--key", eveKey+".pub")
	if _, _, code := inst.ssh(t, eveKey, "", "mr", "retarget", "alice/lib", "1", "release"); code == 0 {
		t.Fatal("a stranger retargeted the merge request")
	}

	if _, errOut, code := inst.ssh(t, aliceKey, "", "mr", "retarget", "alice/lib", "1", "release"); code != 0 {
		t.Fatalf("retarget: %s", errOut)
	}
	show := inst.mrShow(t, aliceKey, "alice/lib", "1")
	if show.TargetRef != "release" {
		t.Fatalf("target not moved: %q", show.TargetRef)
	}
	if len(show.Reviews) != 1 || !show.Reviews[0].Stale {
		t.Fatalf("approval survived the retarget: %+v", show.Reviews)
	}
	// The diff follows the new base: release.txt is on the target now, so
	// it is no longer part of the change.
	out, errOut, code := inst.ssh(t, aliceKey, "", "mr", "diff", "alice/lib", "1")
	if code != 0 {
		t.Fatalf("mr diff: %s", errOut)
	}
	if !strings.Contains(out, "feat.txt") || strings.Contains(out, "release.txt") {
		t.Fatalf("diff not rebased on the new target:\n%s", out)
	}

	// The move is recorded on the conversation.
	if out, _, _ := inst.ssh(t, aliceKey, "", "mr", "show", "alice/lib", "1"); !strings.Contains(out, "retargeted from main to release") {
		t.Fatalf("no system comment for the move:\n%s", out)
	}

	// And the browser can do it, through the same command.
	mrURL := inst.base() + "/alice/lib/mrs/1"
	alice := inst.login(t, aliceKey)
	_, body := browserGet(t, alice, mrURL)
	if !strings.Contains(body, `action="/alice/lib/mrs/1/retarget"`) {
		t.Fatalf("no retarget control on the MR page:\n%s", body)
	}
	if status, _ := browserPost(t, alice, mrURL+"/retarget", url.Values{"target": {"main"}}); status != 200 {
		t.Fatalf("retarget post: %d", status)
	}
	if got := inst.mrShow(t, aliceKey, "alice/lib", "1").TargetRef; got != "main" {
		t.Fatalf("web retarget did not land: %q", got)
	}

	// A merged merge request is settled.
	if _, errOut, code := inst.ssh(t, aliceKey, "", "mr", "merge", "alice/lib", "1"); code != 0 {
		t.Fatalf("merge: %s", errOut)
	}
	if _, errOut, code := inst.ssh(t, aliceKey, "", "mr", "retarget", "alice/lib", "1", "release"); code != 2 ||
		!strings.Contains(errOut, "only an open merge request") {
		t.Fatalf("merged MR retargeted: exit %d, %s", code, errOut)
	}
}
