package e2e

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"
)

// normalizeJSON parses JSON and blanks volatile timestamp fields so the
// remainder can be compared as a golden value.
var tsPat = regexp.MustCompile(`"\d{4}-\d{2}-\d{2}T[0-9:.]+Z?"`)

func golden(t *testing.T, raw string) string {
	t.Helper()
	norm := tsPat.ReplaceAllString(strings.TrimSpace(raw), `"TS"`)
	// Re-encode compactly for stable comparison.
	var v any
	if err := json.Unmarshal([]byte(norm), &v); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, raw)
	}
	out, _ := json.Marshal(v)
	return string(out)
}

func TestIssueLifecycleOverBareSSH(t *testing.T) {
	inst := startInstance(t)

	aliceKey := inst.newKey(t, "alice")
	bobKey := inst.newKey(t, "bob")
	eveKey := inst.newKey(t, "eve")
	inst.admin(t, "admin", "user", "create", "alice", "--key", aliceKey+".pub")
	inst.admin(t, "admin", "user", "create", "bob", "--key", bobKey+".pub")
	inst.admin(t, "admin", "user", "create", "eve", "--key", eveKey+".pub")

	if _, errOut, code := inst.ssh(t, aliceKey, "", "repo", "create", "alice/proj"); code != 0 {
		t.Fatalf("repo create: %s", errOut)
	}

	// Create with inline body; numbering starts at 1.
	out, errOut, code := inst.ssh(t, aliceKey, "",
		"issue", "create", "alice/proj", "--title", "'first bug'", "--body", "'it is broken'", "--json")
	if code != 0 {
		t.Fatalf("issue create: %s", errOut)
	}
	if g := golden(t, out); g != `{"data":{"number":1},"protocol_version":1}` {
		t.Fatalf("create output: %s", g)
	}

	// Bob (no grant, public repo) can file an issue too — body over stdin.
	_, errOut, code = inst.ssh(t, bobKey, "long body\nfrom stdin\n",
		"issue", "create", "alice/proj", "--title", "'from bob'", "--file", "-")
	if code != 0 {
		t.Fatalf("bob issue create: %s", errOut)
	}

	// Comment, label, assign, close.
	if _, errOut, code = inst.ssh(t, bobKey, "", "issue", "comment", "alice/proj", "1", "--message", "'me too'"); code != 0 {
		t.Fatalf("comment: %s", errOut)
	}
	if _, errOut, code = inst.ssh(t, aliceKey, "", "issue", "label", "alice/proj", "1", "--add", "bug", "--add", "urgent"); code != 0 {
		t.Fatalf("label: %s", errOut)
	}
	if _, errOut, code = inst.ssh(t, aliceKey, "", "issue", "assign", "alice/proj", "1", "--add", "bob"); code != 0 {
		t.Fatalf("assign: %s", errOut)
	}

	// Golden check on issue show --json.
	out, _, code = inst.ssh(t, aliceKey, "", "issue", "show", "alice/proj", "1", "--json")
	if code != 0 {
		t.Fatal("issue show failed")
	}
	// A body reports the markup it was written in; "md" is what a body with no
	// --format carries, and what everything written before formats existed has.
	wantShow := `{"data":{"assignees":["bob"],"author":"alice","body":"it is broken",` +
		`"body_format":"md",` +
		`"comments":[{"author":"bob","body":"me too","body_format":"md","created_at":"TS"}],` +
		`"created_at":"TS","labels":["bug","urgent"],"number":1,"state":"open",` +
		`"title":"first bug"},"protocol_version":1}`
	if g := golden(t, out); g != wantShow {
		t.Fatalf("issue show golden mismatch:\ngot  %s\nwant %s", g, wantShow)
	}

	// Permission edges: eve (read-only public) cannot label or close
	// someone else's issue; bob can close his own.
	_, errOut, code = inst.ssh(t, eveKey, "", "issue", "label", "alice/proj", "1", "--add", "spam")
	if code != 4 {
		t.Fatalf("eve label: exit %d (want 4), %s", code, errOut)
	}
	_, errOut, code = inst.ssh(t, eveKey, "", "issue", "close", "alice/proj", "1")
	if code != 4 {
		t.Fatalf("eve close: exit %d (want 4), %s", code, errOut)
	}
	if _, errOut, code = inst.ssh(t, bobKey, "", "issue", "close", "alice/proj", "2"); code != 0 {
		t.Fatalf("bob closing own issue: %s", errOut)
	}

	// Close, list filters, reopen.
	if _, errOut, code = inst.ssh(t, aliceKey, "", "issue", "close", "alice/proj", "1"); code != 0 {
		t.Fatalf("close: %s", errOut)
	}
	out, _, _ = inst.ssh(t, aliceKey, "", "issue", "list", "alice/proj", "--json")
	if g := golden(t, out); g != `{"data":[],"protocol_version":1}` { // empty list is [], never null
		t.Fatalf("open list after close: %s", g)
	}
	out, _, _ = inst.ssh(t, aliceKey, "", "issue", "list", "alice/proj", "--state", "closed", "--json")
	if !strings.Contains(out, `"number":2`) || !strings.Contains(out, `"number":1`) {
		t.Fatalf("closed list: %s", out)
	}
	if _, errOut, code = inst.ssh(t, aliceKey, "", "issue", "reopen", "alice/proj", "1"); code != 0 {
		t.Fatalf("reopen: %s", errOut)
	}
	// Double-reopen is a usage error.
	if _, _, code = inst.ssh(t, aliceKey, "", "issue", "reopen", "alice/proj", "1"); code != 2 {
		t.Fatalf("double reopen: exit %d, want 2", code)
	}

	// Label removal and assignee removal.
	if _, errOut, code = inst.ssh(t, aliceKey, "", "issue", "label", "alice/proj", "1", "--remove", "urgent"); code != 0 {
		t.Fatalf("label remove: %s", errOut)
	}
	if _, errOut, code = inst.ssh(t, aliceKey, "", "issue", "assign", "alice/proj", "1", "--remove", "bob"); code != 0 {
		t.Fatalf("assign remove: %s", errOut)
	}
	out, _, _ = inst.ssh(t, aliceKey, "", "issue", "show", "alice/proj", "1", "--json")
	if strings.Contains(out, "urgent") || strings.Contains(out, "assignees") {
		t.Fatalf("removal not reflected: %s", out)
	}

	// Missing issue and missing repo produce not-found exit codes.
	if _, _, code = inst.ssh(t, aliceKey, "", "issue", "show", "alice/proj", "99"); code != 3 {
		t.Fatalf("missing issue: exit %d, want 3", code)
	}
	if _, _, code = inst.ssh(t, aliceKey, "", "issue", "list", "alice/nope"); code != 3 {
		t.Fatalf("missing repo: exit %d, want 3", code)
	}

	// Web read views: list shows the issue, detail shows the comment, and
	// bodies render as markdown (goldmark drops raw HTML).
	status, body := inst.get(t, "/alice/proj/issues")
	if status != 200 || !strings.Contains(body, "first bug") {
		t.Fatalf("issues page: %d", status)
	}
	status, body = inst.get(t, "/alice/proj/issues/1")
	if status != 200 || !strings.Contains(body, "me too") || !strings.Contains(body, "bug") {
		t.Fatalf("issue detail: %d\n%s", status, body)
	}
	if _, _, code := inst.ssh(t, aliceKey, "", "issue", "create", "alice/proj",
		"--title", "'md body'", "--body", "'has **bold** and <script>x</script>'"); code != 0 {
		t.Fatal("md issue create failed")
	}
	status, body = inst.get(t, "/alice/proj/issues/3")
	if status != 200 || !strings.Contains(body, "<strong>bold</strong>") {
		t.Fatalf("markdown body not rendered: %d\n%s", status, body)
	}
	if strings.Contains(body, "<script>x</script>") {
		t.Fatal("raw HTML survived in issue body")
	}

	// Private repos hide their issues from non-readers, as not-found.
	if _, _, code = inst.ssh(t, aliceKey, "", "repo", "create", "alice/secret", "--private"); code != 0 {
		t.Fatal("create private failed")
	}
	if _, _, code = inst.ssh(t, aliceKey, "", "issue", "create", "alice/secret", "--title", "hidden"); code != 0 {
		t.Fatal("issue in private repo failed")
	}
	if _, _, code = inst.ssh(t, eveKey, "", "issue", "show", "alice/secret", "1"); code != 3 {
		t.Fatalf("eve sees private issue: exit %d, want 3", code)
	}
	if status, _ := inst.get(t, "/alice/secret/issues"); status != 404 {
		t.Fatalf("anonymous issues page on private repo: %d, want 404", status)
	}
}
