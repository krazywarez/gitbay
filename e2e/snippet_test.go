package e2e

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"
)

// Snippets over SSH: create from stdin, read back, list by visibility,
// edit files and metadata, and the not-found rule for private ones.
func TestSnippets(t *testing.T) {
	inst := startInstance(t)
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
	fails := func(key, stdin string, want int, args ...string) string {
		t.Helper()
		_, errOut, code := inst.ssh(t, key, stdin, args...)
		if code != want {
			t.Fatalf("%v: exit %d, want %d: %s", args, code, want, errOut)
		}
		return errOut
	}
	idOf := func(out string) string {
		t.Helper()
		var env struct {
			Data struct {
				ID  string `json:"id"`
				URL string `json:"url"`
			} `json:"data"`
		}
		if err := json.Unmarshal([]byte(out), &env); err != nil || !regexp.MustCompile(`^[0-9a-f]{12}$`).MatchString(env.Data.ID) {
			t.Fatalf("create output: %s", out)
		}
		if !strings.HasSuffix(env.Data.URL, "/alice/-/snippets/"+env.Data.ID) {
			t.Fatalf("url: %s", env.Data.URL)
		}
		return env.Data.ID
	}

	// Create with the default visibility, read back byte for byte.
	body := "line one\nline two\n"
	unlisted := idOf(must(aliceKey, body, "snippet", "create", "build.log", "--description", "'a log'", "--json"))
	if got := must(aliceKey, "", "snippet", "file", "get", unlisted, "build.log"); got != body {
		t.Fatalf("file get: %q", got)
	}
	out := must(aliceKey, "", "snippet", "show", unlisted, "--json")
	if !strings.Contains(out, `"visibility":"unlisted"`) || !strings.Contains(out, `"content":"line one\nline two\n"`) {
		t.Fatalf("show: %s", out)
	}
	public := idOf(must(aliceKey, "pub\n", "snippet", "create", "a.txt", "--visibility", "public", "--json"))
	private := idOf(must(aliceKey, "sec\n", "snippet", "create", "b.txt", "--visibility", "private", "--json"))

	// Refusals on create: empty, not text, over the limit, bad name.
	fails(aliceKey, "", 2, "snippet", "create", "x.txt")
	fails(aliceKey, "\xff\xfe\n", 2, "snippet", "create", "x.bin")
	fails(aliceKey, strings.Repeat("x", 1<<20+1), 2, "snippet", "create", "big.txt")
	fails(aliceKey, "x\n", 2, "snippet", "create", "../x")
	fails(aliceKey, "x\n", 2, "snippet", "create", "x.txt", "--visibility", "secret")

	// Visibility from the other side. Private is not-found, never denied.
	fails(bobKey, "", 3, "snippet", "show", private)
	fails(bobKey, "", 3, "snippet", "file", "get", private, "b.txt")
	must(bobKey, "", "snippet", "show", unlisted)
	out = must(bobKey, "", "snippet", "list", "alice", "--json")
	if !strings.Contains(out, public) || strings.Contains(out, unlisted) || strings.Contains(out, private) {
		t.Fatalf("bob's view of alice's list: %s", out)
	}
	out = must(aliceKey, "", "snippet", "list", "--json")
	for _, id := range []string{public, unlisted, private} {
		if !strings.Contains(out, id) {
			t.Fatalf("alice's own list lacks %s: %s", id, out)
		}
	}
	fails(bobKey, "", 3, "snippet", "list", "nobody")

	// Paging: two pages of one, the second reached by cursor.
	out = must(aliceKey, "", "snippet", "list", "--limit", "1", "--json")
	var page struct {
		Data struct {
			Items []struct {
				ID string `json:"id"`
			} `json:"items"`
			Next string `json:"next"`
		} `json:"data"`
	}
	json.Unmarshal([]byte(out), &page)
	if len(page.Data.Items) != 1 || page.Data.Items[0].ID != private || page.Data.Next == "" {
		t.Fatalf("first page: %s", out)
	}
	out = must(aliceKey, "", "snippet", "list", "--limit", "1", "--cursor", page.Data.Next, "--json")
	if !strings.Contains(out, public) {
		t.Fatalf("second page: %s", out)
	}

	// Files: set adds, set replaces, remove drops, the last one stays.
	must(aliceKey, "notes\n", "snippet", "file", "set", unlisted, "notes.txt")
	must(aliceKey, "changed\n", "snippet", "file", "set", unlisted, "build.log")
	if got := must(aliceKey, "", "snippet", "file", "get", unlisted, "build.log"); got != "changed\n" {
		t.Fatalf("after replace: %q", got)
	}
	must(aliceKey, "", "snippet", "file", "remove", unlisted, "notes.txt")
	fails(aliceKey, "", 3, "snippet", "file", "remove", unlisted, "notes.txt")
	if msg := fails(aliceKey, "", 2, "snippet", "file", "remove", unlisted, "build.log"); !strings.Contains(msg, "at least one file") {
		t.Fatalf("last file removal: %s", msg)
	}

	// Only the owner writes: denied on a readable one, not-found on a private one.
	fails(bobKey, "x\n", 4, "snippet", "file", "set", unlisted, "x.txt")
	fails(bobKey, "", 4, "snippet", "edit", unlisted, "--description", "mine")
	fails(bobKey, "", 4, "snippet", "delete", unlisted)
	fails(bobKey, "", 3, "snippet", "delete", private)

	// Edit moves visibility and the listing follows.
	fails(aliceKey, "", 2, "snippet", "edit", unlisted)
	must(aliceKey, "", "snippet", "edit", unlisted, "--visibility", "public", "--description", "shared")
	out = must(bobKey, "", "snippet", "list", "alice", "--json")
	if !strings.Contains(out, unlisted) || !strings.Contains(out, `"description":"shared"`) {
		t.Fatalf("list after edit: %s", out)
	}

	// Delete, then gone; deleting the user takes the rest.
	must(aliceKey, "", "snippet", "delete", unlisted)
	fails(aliceKey, "", 3, "snippet", "show", unlisted)
	inst.admin(t, "admin", "user", "delete", "alice", "--yes")
	fails(bobKey, "", 3, "snippet", "show", public)
}
