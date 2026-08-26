package e2e

import (
	"net/url"
	"strings"
	"testing"
)

// TestIssueWebTriage closes, labels, assigns, and sets a milestone from
// the browser. Each action runs the issue command the CLI runs, so the
// CLI is the check that they took effect.
func TestIssueWebTriage(t *testing.T) {
	inst := startInstanceWith(t, "[web]\nmode = \"accounts\"\n")
	aliceKey := inst.newKey(t, "alice")
	bobKey := inst.newKey(t, "bob")
	inst.admin(t, "admin", "user", "create", "alice",
		"--key", aliceKey+".pub", "--email", "alice@example.test", "--verified")
	inst.admin(t, "admin", "user", "create", "bob",
		"--key", bobKey+".pub", "--email", "bob@example.test", "--verified")

	if _, errOut, code := inst.ssh(t, aliceKey, "", "repo", "create", "alice/app"); code != 0 {
		t.Fatalf("repo create: %s", errOut)
	}
	if _, errOut, code := inst.ssh(t, aliceKey, "", "milestone", "create", "alice/app", "v1"); code != 0 {
		t.Fatalf("milestone create: %s", errOut)
	}
	if _, errOut, code := inst.ssh(t, aliceKey, "", "issue", "create", "alice/app", "--title", "'needs triage'"); code != 0 {
		t.Fatalf("issue create: %s", errOut)
	}

	alice := inst.login(t, aliceKey)
	issueURL := inst.base() + "/alice/app/issues/1"

	// The triage controls are on the page for someone with write access.
	_, body := browserGet(t, alice, issueURL)
	for _, want := range []string{`/issues/1/state`, `/issues/1/label`, `/issues/1/assign`, `value="v1"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("issue page missing %q:\n%s", want, body)
		}
	}

	// Label, assign, and milestone, each verified through the CLI.
	if status, _ := browserPost(t, alice, issueURL+"/label", url.Values{"add": {"bug ui"}}); status != 200 {
		t.Fatalf("label post: %d", status)
	}
	if status, _ := browserPost(t, alice, issueURL+"/assign", url.Values{"add": {"bob"}}); status != 200 {
		t.Fatalf("assign post: %d", status)
	}
	if status, _ := browserPost(t, alice, issueURL+"/milestone", url.Values{"milestone": {"v1"}}); status != 200 {
		t.Fatalf("milestone post: %d", status)
	}
	out, _, _ := inst.ssh(t, aliceKey, "", "issue", "show", "alice/app", "1", "--json")
	for _, want := range []string{`"bug"`, `"ui"`, `"bob"`, `"v1"`} {
		if !strings.Contains(out, want) {
			t.Fatalf("triage did not land (%s):\n%s", want, out)
		}
	}

	// Removing works the same way.
	if status, _ := browserPost(t, alice, issueURL+"/label", url.Values{"remove": {"ui"}}); status != 200 {
		t.Fatalf("label remove: %d", status)
	}
	out, _, _ = inst.ssh(t, aliceKey, "", "issue", "show", "alice/app", "1", "--json")
	if strings.Contains(out, `"ui"`) {
		t.Fatalf("label not removed:\n%s", out)
	}

	// Close, then reopen.
	if status, _ := browserPost(t, alice, issueURL+"/state", url.Values{"action": {"close"}}); status != 200 {
		t.Fatalf("close post: %d", status)
	}
	if out, _, _ := inst.ssh(t, aliceKey, "", "issue", "show", "alice/app", "1", "--json"); !strings.Contains(out, `"state":"closed"`) {
		t.Fatalf("issue not closed:\n%s", out)
	}
	_, reopened := browserPost(t, alice, issueURL+"/state", url.Values{"action": {"reopen"}})
	if !strings.Contains(reopened, "chip-open") {
		t.Fatalf("issue not reopened:\n%s", reopened)
	}

	// A reader sees no controls and is refused if they forge the post.
	carolKey := inst.newKey(t, "carol")
	inst.admin(t, "admin", "user", "create", "carol", "--key", carolKey+".pub")
	_, readerView := browserGet(t, inst.login(t, carolKey), issueURL)
	if strings.Contains(readerView, `/issues/1/label`) {
		t.Fatal("reader sees triage controls")
	}
	_, denied := browserPost(t, inst.login(t, carolKey), issueURL+"/state", url.Values{"action": {"close"}})
	if !strings.Contains(denied, `class="error"`) {
		t.Fatalf("reader closed someone else's issue:\n%s", denied)
	}
}
