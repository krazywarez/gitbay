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

	_ = url.Values{}
	_ = bobKey
}
