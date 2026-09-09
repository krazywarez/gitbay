package e2e

import (
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestBuildCancelWeb covers cancelling a build from its page (#162). The
// control is offered for anything the command accepts — queued or running —
// and hidden once a build reaches a terminal state; the command decides for
// real, so a stale or repeated post against a build that is no longer
// cancellable shows the refusal rather than a broken page.
func TestBuildCancelWeb(t *testing.T) {
	inst := startInstanceWith(t, "[web]\nmode = \"accounts\"\n")
	aliceKey := inst.newKey(t, "alice")
	inst.admin(t, "admin", "user", "create", "alice",
		"--key", aliceKey+".pub", "--email", "alice@example.test", "--verified")

	runnerKey := inst.newKey(t, "ci-runner")
	pub, _ := os.ReadFile(runnerKey + ".pub")
	if _, errOut, code := inst.ssh(t, aliceKey, "", "repo", "create", "alice/app"); code != 0 {
		t.Fatalf("repo create: %s", errOut)
	}
	// The runner key is attached by alice through repo runner add, which
	// registers it on her account with scope runner, confining it to the
	// runner protocol and read-only git rather than reaching for admin.
	if _, errOut, code := inst.ssh(t, aliceKey, string(pub), "repo", "runner", "add", "alice/app"); code != 0 {
		t.Fatalf("repo runner add: %s", errOut)
	}
	work := t.TempDir()
	env := inst.gitEnv(aliceKey)
	mustGit(t, work, env, "clone", inst.sshURL("alice/app"), "w")
	dir := filepath.Join(work, "w")
	os.MkdirAll(filepath.Join(dir, ".gitbay"), 0o755)
	os.WriteFile(filepath.Join(dir, ".gitbay", "ci.yml"), []byte("jobs:\n  unit:\n    steps:\n      - echo fine\n"), 0o644)
	mustGit(t, dir, env, "checkout", "-q", "-b", "main")
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, env, "commit", "-q", "-m", "ci")
	mustGit(t, dir, env, "push", "-q", "origin", "main")

	alice := inst.login(t, aliceKey)
	build1 := inst.base() + "/alice/app/builds/1"

	// Build 1 is queued: the control is on the page, for someone with
	// write access.
	_, body := browserGet(t, alice, build1)
	if !strings.Contains(body, `action="/alice/app/builds/1/cancel"`) {
		t.Fatalf("no cancel control on a queued build:\n%s", body)
	}
	// A reader gets no control.
	_, anon := browserGet(t, newBrowser(t), build1)
	if strings.Contains(anon, "/cancel") {
		t.Fatal("anonymous visitor sees the cancel control")
	}

	// Cancelling from the page lands where the CLI sees it.
	if status, _ := browserPost(t, alice, build1+"/cancel", url.Values{}); status != 200 {
		t.Fatalf("cancel post: %d", status)
	}
	if out, _, _ := inst.ssh(t, aliceKey, "", "build", "list", "alice/app"); !strings.Contains(out, "unit\tcancelled") {
		t.Fatalf("build not cancelled: %s", out)
	}
	// Cancelled, so the control is gone.
	if _, body = browserGet(t, alice, build1); strings.Contains(body, "/cancel") {
		t.Fatalf("cancel control still on a cancelled build:\n%s", body)
	}

	// Build 2: queued, then claimed by a runner without one actually
	// running any steps — enough to move it to "running".
	if _, errOut, code := inst.ssh(t, aliceKey, "", "build", "trigger", "alice/app", "unit"); code != 0 {
		t.Fatalf("trigger: %s", errOut)
	}
	if out, _, code := inst.ssh(t, runnerKey, "", "runner", "next", "alice/app"); code != 0 || strings.Contains(out, "no pending") {
		t.Fatalf("runner claim: %s", out)
	}
	build2 := inst.base() + "/alice/app/builds/2"
	_, body = browserGet(t, alice, build2)
	if !strings.Contains(body, "running") {
		t.Fatalf("build 2 not running:\n%s", body)
	}
	// The command accepts cancelling a running build too — that is the
	// case #162 was filed for, a run going nowhere — so the control stays
	// up, worded so cancelling does not read as instant.
	if !strings.Contains(body, `action="/alice/app/builds/2/cancel"`) {
		t.Fatalf("no cancel control on a running build:\n%s", body)
	}
	if !strings.Contains(body, "next check") {
		t.Fatalf("cancel control does not warn it is not instant:\n%s", body)
	}

	// Cancelling the running build from the page lands where the CLI sees
	// it, including the runner-facing wording that it stops at its next
	// check rather than right away.
	if status, _ := browserPost(t, alice, build2+"/cancel", url.Values{}); status != 200 {
		t.Fatalf("cancel running build: %d", status)
	}
	out, _, _ := inst.ssh(t, aliceKey, "", "build", "list", "alice/app")
	if !strings.Contains(out, "unit\tcancelled") {
		t.Fatalf("running build not cancelled: %s", out)
	}
	log, _, _ := inst.ssh(t, aliceKey, "", "build", "log", "alice/app", "2")
	if !strings.Contains(log, "cancelled by alice while running") {
		t.Fatalf("log missing the while-running cancellation: %s", log)
	}
	// Cancelled, so the control is gone here too.
	if _, body = browserGet(t, alice, build2); strings.Contains(body, "/cancel") {
		t.Fatalf("cancel control still on a cancelled build:\n%s", body)
	}

	// A stale or repeated post against a build that can no longer be
	// cancelled is refused, and the page says so rather than breaking.
	status, body := browserPost(t, alice, build1+"/cancel", url.Values{})
	if status != 200 {
		t.Fatalf("re-cancel post: %d", status)
	}
	if !strings.Contains(body, `class="error"`) || !strings.Contains(body, "cancelled") {
		t.Fatalf("refusal not surfaced on the page:\n%s", body)
	}
}
