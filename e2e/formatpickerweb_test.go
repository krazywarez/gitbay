package e2e

import (
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestIssueMRWebFormat covers the format picker on the issue and merge
// request create forms (#160). Neither form offered --format before; a
// browser session could only ever write markdown.
func TestIssueMRWebFormat(t *testing.T) {
	inst := startInstanceWith(t, "[web]\nmode = \"accounts\"\n")
	aliceKey := inst.newKey(t, "alice")
	inst.admin(t, "admin", "user", "create", "alice",
		"--key", aliceKey+".pub", "--email", "alice@example.test", "--verified")
	if _, errOut, code := inst.ssh(t, aliceKey, "", "repo", "create", "alice/app"); code != 0 {
		t.Fatalf("repo create: %s", errOut)
	}
	env := inst.gitEnv(aliceKey)
	work := t.TempDir()
	mustGit(t, work, env, "clone", inst.sshURL("alice/app"), "w")
	dir := filepath.Join(work, "w")
	mustGit(t, dir, env, "checkout", "-q", "-b", "main")
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a\n"), 0o644)
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, env, "commit", "-q", "-m", "base")
	mustGit(t, dir, env, "push", "-q", "origin", "main")
	mustGit(t, dir, env, "checkout", "-q", "-b", "topic")
	os.WriteFile(filepath.Join(dir, "b.txt"), []byte("b\n"), 0o644)
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, env, "commit", "-q", "-m", "topic")
	mustGit(t, dir, env, "push", "-q", "origin", "topic")

	alice := inst.login(t, aliceKey)
	base := inst.base() + "/alice/app"
	const orgBody = "Some /emphasis/ here.\n"

	// The picker is on both forms, defaulting to Markdown.
	for _, form := range []string{base + "/issues/new", base + "/mrs/new"} {
		if _, body := browserGet(t, alice, form); !strings.Contains(body, `name="format"`) ||
			!strings.Contains(body, `<option value="md" selected>`) {
			t.Fatalf("no format picker (defaulting to md) on %s:\n%s", form, body)
		}
	}

	// An issue written with the org choice renders as org, not markdown.
	if status, _ := browserPost(t, alice, base+"/issues/new", url.Values{
		"title": {"org issue"}, "body": {orgBody}, "format": {"org"},
	}); status != 200 {
		t.Fatalf("issue create: %d", status)
	}
	if _, body := browserGet(t, alice, base+"/issues/1"); !strings.Contains(body, "<em>emphasis</em>") {
		t.Fatalf("issue body did not render as org:\n%s", body)
	}

	// A merge request written with the org choice renders as org too.
	if status, _ := browserPost(t, alice, base+"/mrs/new", url.Values{
		"source": {"topic"}, "target": {"main"}, "title": {"org mr"}, "body": {orgBody}, "format": {"org"},
	}); status != 200 {
		t.Fatalf("mr create: %d", status)
	}
	if _, body := browserGet(t, alice, base+"/mrs/1"); !strings.Contains(body, "<em>emphasis</em>") {
		t.Fatalf("mr body did not render as org:\n%s", body)
	}

	// A form left on the default choice still writes markdown.
	if status, _ := browserPost(t, alice, base+"/issues/new", url.Values{
		"title": {"md issue"}, "body": {orgBody}, "format": {"md"},
	}); status != 200 {
		t.Fatalf("issue create: %d", status)
	}
	if _, body := browserGet(t, alice, base+"/issues/2"); strings.Contains(body, "<em>emphasis</em>") {
		t.Fatalf("issue body rendered as org despite the md choice:\n%s", body)
	}
}
