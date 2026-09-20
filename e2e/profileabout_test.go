package e2e

import (
	"strings"
	"testing"
)

// The about text is a file in <owner>/.gitbay, read on every surface with
// the reader's own access.
func TestProfileAboutFromRepo(t *testing.T) {
	inst := startInstance(t)
	aliceKey := inst.newKey(t, "alice")
	inst.admin(t, "admin", "user", "create", "alice",
		"--key", aliceKey+".pub", "--email", "alice@example.test", "--verified")
	bobKey := inst.newKey(t, "bob")
	inst.admin(t, "admin", "user", "create", "bob",
		"--key", bobKey+".pub", "--email", "bob@example.test", "--verified")

	if _, _, code := inst.ssh(t, aliceKey, "", "repo", "create", "alice/.gitbay"); code != 0 {
		t.Fatal("creating alice/.gitbay failed")
	}
	if _, _, code := inst.ssh(t, aliceKey, "# alice\n\nhello from a file\n",
		"repo", "commit-file", "alice/.gitbay", "profile/README.md",
		"--ref", "main", "--file", "-"); code != 0 {
		t.Fatal("committing the about failed")
	}

	out, _, code := inst.ssh(t, bobKey, "", "profile", "show", "alice", "--json")
	if code != 0 {
		t.Fatalf("profile show: %d", code)
	}
	if !strings.Contains(out, "hello from a file") {
		t.Errorf("about not read from the repository: %s", out)
	}
	if !strings.Contains(out, `"about_format":"md"`) {
		t.Errorf("about_format not md: %s", out)
	}
	if !strings.Contains(out, `"about_path":"profile/README.md"`) {
		t.Errorf("about_path missing: %s", out)
	}

	_, body := inst.get(t, "/alice")
	if !strings.Contains(body, "hello from a file") {
		t.Error("web profile does not render the about")
	}

	// The extension drives the renderer: an .org about renders as org.
	if _, _, code := inst.ssh(t, bobKey, "", "repo", "create", "bob/.gitbay"); code != 0 {
		t.Fatal("creating bob/.gitbay failed")
	}
	if _, errOut, code := inst.ssh(t, bobKey, "a /note/ in org\n",
		"repo", "commit-file", "bob/.gitbay", "profile/README.org",
		"--ref", "main", "--file", "-"); code != 0 {
		t.Fatalf("committing bob's org about: %s", errOut)
	}
	if _, page := inst.get(t, "/bob"); !strings.Contains(page, "<em>note</em>") {
		t.Errorf("about did not render as org:\n%s", page)
	}
}

// The extension picks the format, .md wins the resolution order, and a
// private .gitbay keeps the about to the people who can read it.
func TestProfileAboutFormatAndPrivacy(t *testing.T) {
	inst := startInstance(t)
	aliceKey := inst.newKey(t, "alice")
	inst.admin(t, "admin", "user", "create", "alice",
		"--key", aliceKey+".pub", "--email", "alice@example.test", "--verified")
	bobKey := inst.newKey(t, "bob")
	inst.admin(t, "admin", "user", "create", "bob",
		"--key", bobKey+".pub", "--email", "bob@example.test", "--verified")

	inst.ssh(t, aliceKey, "", "repo", "create", "alice/.gitbay", "--private")
	if _, _, code := inst.ssh(t, aliceKey, "* heading\n\norg text here\n",
		"repo", "commit-file", "alice/.gitbay", "profile/README.org",
		"--ref", "main", "--file", "-"); code != 0 {
		t.Fatal("committing the org about failed")
	}

	out, _, _ := inst.ssh(t, aliceKey, "", "profile", "show", "alice", "--json")
	if !strings.Contains(out, "org text here") || !strings.Contains(out, `"about_format":"org"`) {
		t.Errorf("owner cannot read their own private about: %s", out)
	}

	out, _, code := inst.ssh(t, bobKey, "", "profile", "show", "alice", "--json")
	if code != 0 {
		t.Fatalf("profile show for an outsider should succeed: %d", code)
	}
	if strings.Contains(out, "org text here") {
		t.Errorf("private about leaked to an outsider: %s", out)
	}

	// A .md beside the .org wins: it is first in the resolution order.
	if _, _, code := inst.ssh(t, aliceKey, "markdown wins\n",
		"repo", "commit-file", "alice/.gitbay", "profile/README.md",
		"--ref", "main", "--file", "-"); code != 0 {
		t.Fatal("committing the md about failed")
	}
	out, _, _ = inst.ssh(t, aliceKey, "", "profile", "show", "alice", "--json")
	if !strings.Contains(out, "markdown wins") {
		t.Errorf(".md did not win resolution: %s", out)
	}
}

// The about is not settable through profile set any more: it is a file.
func TestProfileSetHasNoAbout(t *testing.T) {
	inst := startInstance(t)
	aliceKey := inst.newKey(t, "alice")
	inst.admin(t, "admin", "user", "create", "alice",
		"--key", aliceKey+".pub", "--email", "alice@example.test", "--verified")

	if _, _, code := inst.ssh(t, aliceKey, "", "profile", "set", "--about", "'inline text'"); code == 0 {
		t.Error("profile set --about still accepted")
	}
	if _, _, code := inst.ssh(t, aliceKey, "x", "profile", "set", "--file", "-"); code == 0 {
		t.Error("profile set --file still accepted")
	}

	// The flags that stay still work.
	if _, errOut, code := inst.ssh(t, aliceKey, "",
		"profile", "set", "--description", "'a line'", "--link", "'site|https://example.org'"); code != 0 {
		t.Fatalf("profile set --description --link: %s", errOut)
	}
	out, _, _ := inst.ssh(t, aliceKey, "", "profile", "show", "alice", "--json")
	if !strings.Contains(out, "a line") || !strings.Contains(out, "https://example.org") {
		t.Errorf("description or link not saved: %s", out)
	}
}
