package e2e

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func snippetIDFrom(t *testing.T, out string) string {
	t.Helper()
	var env struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil || env.Data.ID == "" {
		t.Fatalf("snippet create: %s", out)
	}
	return env.Data.ID
}

// Snippet pages: the owner's list, one snippet with highlighted files, the
// raw route, the owner-page link, and 404 for what the viewer may not see.
func TestSnippetsWeb(t *testing.T) {
	inst := startInstanceWith(t, "[web]\nmode = \"accounts\"\n")
	aliceKey := inst.newKey(t, "alice")
	bobKey := inst.newKey(t, "bob")
	inst.admin(t, "admin", "user", "create", "alice", "--key", aliceKey+".pub", "--email", "alice@example.test", "--verified")
	inst.admin(t, "admin", "user", "create", "bob", "--key", bobKey+".pub", "--email", "bob@example.test", "--verified")
	must := func(key, stdin string, args ...string) string {
		t.Helper()
		out, errOut, code := inst.ssh(t, key, stdin, args...)
		if code != 0 {
			t.Fatalf("%v: exit %d %s", args, code, errOut)
		}
		return out
	}
	public := snippetIDFrom(t, must(aliceKey, "package main\n", "snippet", "create", "main.go", "--visibility", "public", "--description", "'hello world'", "--json"))
	unlisted := snippetIDFrom(t, must(aliceKey, "quiet\n", "snippet", "create", "q.txt", "--json"))
	private := snippetIDFrom(t, must(aliceKey, "secret\n", "snippet", "create", "s.txt", "--visibility", "private", "--json"))

	// Anonymous: the public list, the unlisted page by URL, 404 for private.
	status, body := inst.get(t, "/alice/-/snippets")
	if status != 200 || !strings.Contains(body, public) || strings.Contains(body, unlisted) || strings.Contains(body, private) {
		t.Fatalf("anonymous list: %d\n%s", status, body)
	}
	status, body = inst.get(t, "/alice/-/snippets/"+public)
	if status != 200 || !strings.Contains(body, "hello world") || !strings.Contains(body, `class="chroma"`) || !strings.Contains(body, "/raw/main.go") {
		t.Fatalf("public page: %d\n%s", status, body)
	}
	if status, _ := inst.get(t, "/alice/-/snippets/"+unlisted); status != 200 {
		t.Fatalf("unlisted page: %d", status)
	}
	if status, _ := inst.get(t, "/alice/-/snippets/"+private); status != 404 {
		t.Fatalf("private page for anonymous: %d", status)
	}
	if status, _ := inst.get(t, "/bob/-/snippets/"+public); status != 404 {
		t.Fatalf("id under the wrong owner: %d", status)
	}
	if status, _ := inst.get(t, "/nobody/-/snippets"); status != 404 {
		t.Fatalf("list for a missing owner: %d", status)
	}

	// Raw is text/plain with nosniff, whatever the extension.
	resp, err := http.Get(inst.base() + "/alice/-/snippets/" + public + "/raw/main.go")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/plain") || resp.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("raw headers: %d %v", resp.StatusCode, resp.Header)
	}
	if status, _ := inst.get(t, "/alice/-/snippets/" + public + "/raw/other.go"); status != 404 {
		t.Fatalf("raw for a missing file: %d", status)
	}

	// The owner sees everything with visibility marks; the owner page links.
	alice := inst.login(t, aliceKey)
	status, body = browserGet(t, alice, inst.base()+"/alice/-/snippets")
	if status != 200 || !strings.Contains(body, private) || !strings.Contains(body, ">private<") {
		t.Fatalf("owner list: %d\n%s", status, body)
	}
	if status, body := browserGet(t, alice, inst.base()+"/alice/-/snippets/"+private); status != 200 || !strings.Contains(body, "secret") {
		t.Fatalf("owner's private page: %d", status)
	}
	if status, body := inst.get(t, "/alice"); status != 200 || !strings.Contains(body, `href="/alice/-/snippets"`) {
		t.Fatalf("owner page lacks the snippets link: %d", status)
	}
	// bob has no public snippets and is not the viewer: no link.
	if status, body := inst.get(t, "/bob"); status != 200 || strings.Contains(body, `href="/bob/-/snippets"`) {
		t.Fatalf("bob's page shows a snippets link with nothing to list: %d", status)
	}

	// The create form makes a snippet through snippet create.
	status, body = browserPost(t, alice, inst.base()+"/alice/-/snippets/new", url.Values{
		"name": {"notes.md"}, "description": {"from the browser"}, "visibility": {"public"}, "content": {"# notes\n"}})
	if status != 200 || !strings.Contains(body, "from the browser") || !strings.Contains(body, "notes.md") {
		t.Fatalf("create form: %d\n%s", status, body)
	}
	var listed struct {
		Data []struct {
			ID          string `json:"id"`
			Description string `json:"description"`
		} `json:"data"`
	}
	json.Unmarshal([]byte(must(aliceKey, "", "snippet", "list", "--json")), &listed)
	created := ""
	for _, sn := range listed.Data {
		if sn.Description == "from the browser" {
			created = sn.ID
		}
	}
	if created == "" {
		t.Fatalf("created from the web, not listed: %+v", listed.Data)
	}
	if status, _ := browserGet(t, alice, inst.base()+"/bob/-/snippets/new"); status != 404 {
		t.Fatalf("new form under another owner: %d", status)
	}

	// A refused create re-renders the form with the paste kept, not a
	// bare error page.
	_, body = browserPost(t, alice, inst.base()+"/alice/-/snippets/new", url.Values{
		"name": {"../x"}, "content": {"kept content\n"}})
	if !strings.Contains(body, `class="error"`) || !strings.Contains(body, "kept content") {
		t.Fatalf("refused create form:\n%s", body)
	}

	// The file form replaces a file and adds one; remove drops it.
	page := inst.base() + "/alice/-/snippets/" + created
	if status, _ := browserPost(t, alice, page+"/file", url.Values{"name": {"notes.md"}, "content": {"# changed\n"}}); status != 200 {
		t.Fatal("file replace failed")
	}
	if got := must(aliceKey, "", "snippet", "file", "get", created, "notes.md"); got != "# changed\n" {
		t.Fatalf("after web replace: %q", got)
	}
	if status, _ := browserPost(t, alice, page+"/file", url.Values{"name": {"b.txt"}, "content": {"b\n"}}); status != 200 {
		t.Fatal("file add failed")
	}
	// Removing a file needs its name typed; a bare post is refused and
	// the file stays.
	_, body = browserPost(t, alice, page+"/file/remove", url.Values{"name": {"b.txt"}})
	if !strings.Contains(body, "type b.txt to confirm") {
		t.Fatalf("unconfirmed file remove was not refused:\n%s", body)
	}
	if _, _, code := inst.ssh(t, aliceKey, "", "snippet", "file", "get", created, "b.txt"); code != 0 {
		t.Fatalf("b.txt removed without confirmation: exit %d", code)
	}
	if status, _ := browserPost(t, alice, page+"/file/remove", url.Values{"name": {"b.txt"}, "confirm": {"b.txt"}}); status != 200 {
		t.Fatal("file remove failed")
	}
	if _, _, code := inst.ssh(t, aliceKey, "", "snippet", "file", "get", created, "b.txt"); code != 3 {
		t.Fatalf("b.txt after web remove: exit %d", code)
	}
	// A refusal comes back on the page as a message, not a bare error.
	// The confirmation matches, so the refusal under test is still the
	// command's last-file rule.
	_, body = browserPost(t, alice, page+"/file/remove", url.Values{"name": {"notes.md"}, "confirm": {"notes.md"}})
	if !strings.Contains(body, `class="error"`) || !strings.Contains(body, "at least one file") {
		t.Fatalf("last-file refusal on the page:\n%s", body)
	}

	// Edit changes visibility; delete removes.
	if status, _ := browserPost(t, alice, page+"/edit", url.Values{"description": {"renamed"}, "visibility": {"private"}}); status != 200 {
		t.Fatal("edit failed")
	}
	if status, _ := inst.get(t, "/alice/-/snippets/"+created); status != 404 {
		t.Fatalf("private after web edit, anonymous: %d", status)
	}
	// bob cannot write alice's snippet from the browser either.
	bob := inst.login(t, bobKey)
	// A logged-in stranger sees the same visibility rule as anonymous:
	// 404 for a private snippet, 200 for an unlisted one.
	if status, _ := browserGet(t, bob, inst.base()+"/alice/-/snippets/"+private); status != 404 {
		t.Fatalf("stranger on a private page: %d", status)
	}
	if status, _ := browserGet(t, bob, inst.base()+"/alice/-/snippets/"+unlisted); status != 200 {
		t.Fatalf("stranger on an unlisted page: %d", status)
	}
	// An unconfirmed delete on a private snippet is still 404 for a
	// stranger: the snippet is resolved, and refused, before the
	// confirmation is even checked.
	if status, _ := browserPost(t, bob, inst.base()+"/alice/-/snippets/"+private+"/delete", nil); status != 404 {
		t.Fatalf("stranger's unconfirmed delete on a private snippet: %d", status)
	}
	if status, _ := browserPost(t, bob, inst.base()+"/alice/-/snippets/"+public+"/edit", url.Values{"description": {"x"}, "visibility": {"public"}}); status != 403 {
		t.Fatalf("bob editing alice's snippet: %d", status)
	}
	// Deleting needs the public id typed to confirm; a bare post leaves
	// the snippet in place.
	_, body = browserPost(t, alice, page+"/delete", nil)
	if !strings.Contains(body, "type "+created+" to confirm") {
		t.Fatalf("unconfirmed delete was not refused:\n%s", body)
	}
	if _, _, code := inst.ssh(t, aliceKey, "", "snippet", "show", created); code != 0 {
		t.Fatalf("snippet deleted without confirmation: exit %d", code)
	}
	if status, _ := browserPost(t, alice, page+"/delete", url.Values{"confirm": {created}}); status != 200 {
		t.Fatal("delete failed")
	}
	if _, _, code := inst.ssh(t, aliceKey, "", "snippet", "show", created); code != 3 {
		t.Fatalf("after web delete: exit %d", code)
	}
}
