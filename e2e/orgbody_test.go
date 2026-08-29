package e2e

import (
	"strings"
	"testing"
)

// A body written in org renders as org, end to end: the format is chosen over
// SSH, stored with the text, and honoured when the page is built. A body
// without a format is markdown, which is what everything written before the
// format existed carries.
func TestOrgBodies(t *testing.T) {
	inst := startInstance(t)
	aliceKey := inst.newKey(t, "alice")
	inst.admin(t, "admin", "user", "create", "alice",
		"--key", aliceKey+".pub", "--email", "alice@example.test", "--verified")

	if _, errOut, code := inst.ssh(t, aliceKey, "", "repo", "create", "alice/app"); code != 0 {
		t.Fatalf("repo create: %s", errOut)
	}

	const orgBody = "* A heading\n\nSome /emphasis/ and =code= here.\n"

	// #1 is org, #2 is the same text left as markdown.
	if _, errOut, code := inst.ssh(t, aliceKey, orgBody, "issue", "create", "alice/app",
		"--title", "'org issue'", "--file", "-", "--format", "org"); code != 0 {
		t.Fatalf("issue create --format org: %s", errOut)
	}
	if _, errOut, code := inst.ssh(t, aliceKey, orgBody, "issue", "create", "alice/app",
		"--title", "'md issue'", "--file", "-"); code != 0 {
		t.Fatalf("issue create: %s", errOut)
	}

	status, body := inst.get(t, "/alice/app/issues/1")
	if status != 200 {
		t.Fatalf("issue 1: status %d", status)
	}
	if !strings.Contains(body, "<em>emphasis</em>") || !strings.Contains(body, "<code>code</code>") {
		t.Fatalf("org body did not render as org:\n%s", body)
	}
	// A remark is not a document: no table of contents above two headings.
	if strings.Contains(body, `href="#headline-1"`) {
		t.Fatalf("org body grew a table of contents:\n%s", body)
	}

	status, body = inst.get(t, "/alice/app/issues/2")
	if status != 200 {
		t.Fatalf("issue 2: status %d", status)
	}
	// Markdown leaves org markup alone; the heading stays literal text.
	if strings.Contains(body, "<em>emphasis</em>") {
		t.Fatalf("markdown body rendered as org:\n%s", body)
	}

	// Comments carry their own format, independent of the issue's.
	if _, errOut, code := inst.ssh(t, aliceKey, "A /commented/ remark.\n",
		"issue", "comment", "alice/app", "2", "--file", "-", "--format", "org"); code != 0 {
		t.Fatalf("issue comment --format org: %s", errOut)
	}
	if status, body = inst.get(t, "/alice/app/issues/2"); status != 200 ||
		!strings.Contains(body, "<em>commented</em>") {
		t.Fatalf("org comment on a markdown issue did not render as org:\n%s", body)
	}

	// An edit that does not mention a format leaves the stored one alone.
	if _, errOut, code := inst.ssh(t, aliceKey, "", "issue", "edit", "alice/app", "1",
		"--title", "'org issue, retitled'"); code != 0 {
		t.Fatalf("issue edit: %s", errOut)
	}
	if status, body = inst.get(t, "/alice/app/issues/1"); status != 200 ||
		!strings.Contains(body, "<em>emphasis</em>") {
		t.Fatalf("editing the title dropped the body's org format:\n%s", body)
	}

	// The format is reported over the API, so other surfaces can honour it.
	out, errOut, code := inst.ssh(t, aliceKey, "", "issue", "show", "alice/app", "1", "--json")
	if code != 0 {
		t.Fatalf("issue show --json: %s", errOut)
	}
	if !strings.Contains(out, `"body_format":"org"`) {
		t.Fatalf("issue show did not report the body format:\n%s", out)
	}
}
