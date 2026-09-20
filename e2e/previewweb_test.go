package e2e

import (
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Every markup form has a Preview button that renders the draft above
// the textarea and writes nothing (#235). The forms are: issue and MR
// create, their edit and comment boxes, release create and edit, the
// profile about text, and the file editor on a path the forge renders.
func TestMarkupPreviewWeb(t *testing.T) {
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
	os.WriteFile(filepath.Join(dir, "README.md"), []byte("# hi\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a\n"), 0o644)
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, env, "commit", "-q", "-m", "base")
	mustGit(t, dir, env, "push", "-q", "origin", "main")
	mustGit(t, dir, env, "checkout", "-q", "-b", "topic")
	os.WriteFile(filepath.Join(dir, "b.txt"), []byte("b\n"), 0o644)
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, env, "commit", "-q", "-m", "topic")
	mustGit(t, dir, env, "push", "-q", "origin", "topic")
	mustGit(t, dir, env, "tag", "v1")
	mustGit(t, dir, env, "push", "-q", "origin", "v1")

	alice := inst.login(t, aliceKey)
	base := inst.base() + "/alice/app"

	post := func(where string, v url.Values) string {
		t.Helper()
		status, body := browserPost(t, alice, where, v)
		if status != 200 {
			t.Fatalf("post %s %v: %d", where, v, status)
		}
		return body
	}
	// previewed asserts the rendering is on the page and the draft came
	// back with it.
	previewed := func(what, body, want, kept string) {
		t.Helper()
		if !strings.Contains(body, `<p class="previewmark">Preview</p>`) {
			t.Fatalf("%s: no preview block:\n%s", what, body)
		}
		if !strings.Contains(body, want) {
			t.Fatalf("%s: preview does not contain %q:\n%s", what, want, body)
		}
		if kept != "" && !strings.Contains(body, kept) {
			t.Fatalf("%s: form lost %q:\n%s", what, kept, body)
		}
	}

	// Issue create: markdown renders, and the title and labels survive.
	b := post(base+"/issues/new", url.Values{
		"title": {"draft title"}, "body": {"# heading\n"}, "labels": {"bug"},
		"format": {"md"}, "preview": {"1"},
	})
	previewed("issue create", b, "<h1", `value="draft title"`)
	if !strings.Contains(b, `value="bug"`) {
		t.Fatalf("issue create preview lost the labels:\n%s", b)
	}
	// Nothing was opened.
	if out, _, _ := inst.ssh(t, aliceKey, "", "issue", "list", "alice/app", "--json"); strings.Contains(out, "draft title") {
		t.Fatalf("preview created an issue: %s", out)
	}

	// The org choice previews as org, not markdown.
	b = post(base+"/issues/new", url.Values{
		"title": {"t"}, "body": {"Some /emphasis/ here.\n"}, "format": {"org"}, "preview": {"1"},
	})
	previewed("issue create org", b, "<em>emphasis</em>", `<option value="org" selected>`)

	// A real issue, then its comment and edit boxes.
	if _, errOut, code := inst.ssh(t, aliceKey, "", "issue", "create", "alice/app", "--title", "real", "--body", "body"); code != 0 {
		t.Fatalf("issue create: %s", errOut)
	}
	b = post(base+"/issues/1/comment", url.Values{"body": {"a **bold** remark"}, "preview": {"1"}})
	previewed("issue comment", b, "<strong>bold</strong>", "a **bold** remark")
	b = post(base+"/issues/1/edit", url.Values{
		"title": {"real"}, "body": {"## edited"}, "preview": {"1"},
	})
	previewed("issue edit", b, "<h2", "## edited")
	if !strings.Contains(b, `<details class="editbox" open>`) {
		t.Fatalf("issue edit preview left the box shut:\n%s", b)
	}
	// Neither wrote: one comment would show in the thread, an edit in the body.
	if out, _, _ := inst.ssh(t, aliceKey, "", "issue", "show", "alice/app", "1", "--json"); strings.Contains(out, "bold") || strings.Contains(out, "edited") {
		t.Fatalf("preview wrote to the issue: %s", out)
	}

	// MR create, comment and edit.
	b = post(base+"/mrs/new", url.Values{
		"source": {"topic"}, "target": {"main"}, "title": {"mr draft"},
		"body": {"# mr heading"}, "format": {"md"}, "preview": {"1"},
	})
	previewed("mr create", b, "<h1", `value="mr draft"`)
	if out, _, _ := inst.ssh(t, aliceKey, "", "mr", "list", "alice/app", "--json"); strings.Contains(out, "mr draft") {
		t.Fatalf("preview opened a merge request: %s", out)
	}
	if _, errOut, code := inst.ssh(t, aliceKey, "", "mr", "create", "alice/app",
		"--source", "topic", "--target", "main", "--title", "'real mr'"); code != 0 {
		t.Fatalf("mr create: %s", errOut)
	}
	b = post(base+"/mrs/1/comment", url.Values{"body": {"a `code` remark"}, "preview": {"1"}})
	previewed("mr comment", b, "<code>code</code>", "a `code` remark")
	b = post(base+"/mrs/1/edit", url.Values{"title": {"real mr"}, "body": {"## mr edited"}, "preview": {"1"}})
	previewed("mr edit", b, "<h2", "## mr edited")

	// Release create, then edit.
	b = post(base+"/releases", url.Values{
		"tag": {"v1"}, "title": {"first"}, "notes": {"* a bullet"}, "preview": {"1"},
	})
	previewed("release create", b, "<li>", "* a bullet")
	if out, _, _ := inst.ssh(t, aliceKey, "", "release", "list", "alice/app", "--json"); strings.Contains(out, "first") {
		t.Fatalf("preview created a release: %s", out)
	}
	if _, errOut, code := inst.ssh(t, aliceKey, "", "release", "create", "alice/app", "v1", "--title", "first"); code != 0 {
		t.Fatalf("release create: %s", errOut)
	}
	b = post(base+"/releases", url.Values{
		"action": {"edit"}, "tag": {"v1"}, "title": {"first"},
		"notes": {"## release notes"}, "preview": {"1"},
	})
	previewed("release edit", b, "<h2", "## release notes")

	// The profile form takes no markup: the about text is a file, and the
	// file editor below is what previews it.
	if _, body := browserGet(t, alice, inst.base()+"/settings"); strings.Contains(body, `name="preview"`) {
		t.Fatalf("the profile form offers a preview:\n%s", body)
	}

	// The file editor previews a rendered path and offers nothing on one
	// it does not render.
	b = post(base+"/edit/main/README.md", url.Values{
		"content": {"# from the editor"}, "message": {"kept message"}, "preview": {"1"},
	})
	previewed("file editor", b, "from the editor", `value="kept message"`)
	if _, body := browserGet(t, alice, base+"/blob/main/README.md"); strings.Contains(body, "from the editor") {
		t.Fatalf("preview committed the file:\n%s", body)
	}
	if _, body := browserGet(t, alice, base+"/edit/main/a.txt"); strings.Contains(body, `name="preview"`) {
		t.Fatalf("a plain text file offers a preview:\n%s", body)
	}
	if _, body := browserGet(t, alice, base+"/edit/main/README.md"); !strings.Contains(body, `name="preview"`) {
		t.Fatalf("no preview button on a markdown file:\n%s", body)
	}
}
