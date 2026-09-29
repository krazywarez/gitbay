package e2e

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Saved queries over stock ssh: a query spans the repositories the
// caller reads, never another user's private one, pages with the
// cursor, and a pinned one reaches the dashboard and its web page (#292).
func TestSavedQueries(t *testing.T) {
	t.Parallel()
	inst := startInstanceWith(t, "[web]\nmode = \"accounts\"\n")
	aliceKey := inst.newKey(t, "alice")
	bobKey := inst.newKey(t, "bob")
	inst.admin(t, "admin", "user", "create", "alice",
		"--key", aliceKey+".pub", "--email", "alice@example.test", "--verified")
	inst.admin(t, "admin", "user", "create", "bob", "--key", bobKey+".pub")
	ssh := func(key string, want int, args ...string) string {
		t.Helper()
		out, errOut, code := inst.ssh(t, key, "", args...)
		if code != want {
			t.Fatalf("%v: exit %d, want %d\n%s%s", args, code, want, out, errOut)
		}
		return out + errOut
	}

	ssh(aliceKey, 0, "repo", "create", "alice/app")
	ssh(aliceKey, 0, "repo", "create", "alice/lib")
	ssh(bobKey, 0, "repo", "create", "bob/secret", "--private")
	work := t.TempDir()
	env := inst.gitEnv(aliceKey)
	mustGit(t, work, env, "clone", inst.sshURL("alice/app"), "w")
	dir := filepath.Join(work, "w")
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a\n"), 0o644)
	mustGit(t, dir, env, "checkout", "-q", "-b", "main")
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, env, "commit", "-q", "-m", "base")
	mustGit(t, dir, env, "push", "-q", "origin", "main")
	mustGit(t, dir, env, "checkout", "-q", "-b", "feat")
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a\nb\n"), 0o644)
	mustGit(t, dir, env, "commit", "-q", "-am", "feat")
	mustGit(t, dir, env, "push", "-q", "origin", "feat")
	ssh(aliceKey, 0, "mr", "create", "alice/app", "--source", "feat", "--target", "main", "--title", "feature")
	ssh(aliceKey, 0, "issue", "create", "alice/app", "--title", "app-bug", "--label", "bug")
	ssh(aliceKey, 0, "issue", "create", "alice/lib", "--title", "lib-bug", "--label", "bug")
	ssh(bobKey, 0, "issue", "create", "bob/secret", "--title", "hidden-bug", "--label", "bug")

	if out := ssh(aliceKey, 2, "query", "save", "bad", "is:open", "bogus:x"); !strings.Contains(out, "bogus:x") {
		t.Errorf("a bad term is not named: %s", out)
	}
	ssh(aliceKey, 0, "query", "save", "bugs", "'repo:alice/*'", "label:bug", "is:open")
	ssh(aliceKey, 1, "query", "save", "bugs", "is:open")
	ssh(aliceKey, 0, "query", "save", "all", "is:open")
	ssh(bobKey, 0, "query", "save", "all", "is:open")

	type page struct {
		Data struct {
			Items []struct {
				Kind  string `json:"kind"`
				Repo  string `json:"repo"`
				Title string `json:"title"`
			} `json:"items"`
			Next string `json:"next"`
		} `json:"data"`
	}
	run := func(key string, args ...string) page {
		t.Helper()
		var p page
		if err := json.Unmarshal([]byte(ssh(key, 0, append(args, "--json")...)), &p); err != nil {
			t.Fatal(err)
		}
		return p
	}
	titles := func(p page) string {
		var out []string
		for _, it := range p.Data.Items {
			out = append(out, it.Repo+":"+it.Title)
		}
		return strings.Join(out, " ")
	}

	if got := titles(run(aliceKey, "issue", "list", "--query", "bugs")); got != "alice/lib:lib-bug alice/app:app-bug" {
		t.Errorf("issue list --query bugs = %s", got)
	}
	if got := titles(run(aliceKey, "mr", "list", "--query", "all")); got != "alice/app:feature" {
		t.Errorf("mr list --query all = %s", got)
	}
	if got := titles(run(bobKey, "query", "run", "all")); !strings.Contains(got, "hidden-bug") {
		t.Errorf("bob's own private issue is missing from his query: %s", got)
	}

	// alice pages through everything she reads, one row at a time.
	var seen []string
	cursor := ""
	for i := 0; i < 6; i++ {
		args := []string{"query", "run", "all", "--limit", "1"}
		if cursor != "" {
			args = append(args, "--cursor", cursor)
		}
		p := run(aliceKey, args...)
		seen = append(seen, titles(p))
		if cursor = p.Data.Next; cursor == "" {
			break
		}
	}
	if got := strings.Join(seen, " "); got != "alice/lib:lib-bug alice/app:app-bug alice/app:feature" {
		t.Errorf("paged run = %s", got)
	}

	ssh(aliceKey, 0, "query", "pin", "all")
	var dash struct {
		Data struct {
			Queries []struct {
				Name  string `json:"name"`
				Count int    `json:"count"`
			} `json:"queries"`
		} `json:"data"`
	}
	json.Unmarshal([]byte(ssh(aliceKey, 0, "dashboard", "--json")), &dash)
	if len(dash.Data.Queries) != 1 || dash.Data.Queries[0].Name != "all" || dash.Data.Queries[0].Count != 3 {
		t.Errorf("dashboard queries = %+v; want all with 3, bob's private issue uncounted", dash.Data.Queries)
	}

	out := ssh(aliceKey, 0, "web", "login", "--json")
	var login struct {
		Data struct {
			URL string `json:"url"`
		} `json:"data"`
	}
	json.Unmarshal([]byte(out), &login)
	browser := newBrowser(t)
	if status, _ := browserGet(t, browser, inst.base()+login.Data.URL[strings.Index(login.Data.URL, "/login"):]); status != 200 {
		t.Fatalf("login: %d", status)
	}
	_, body := browserGet(t, browser, inst.base()+"/")
	if !strings.Contains(body, `<a href="/alice/-/queries/all">all</a> <span class="count">3</span>`) {
		t.Errorf("dashboard lacks the pinned query:\n%s", body)
	}
	status, body := browserGet(t, browser, inst.base()+"/alice/-/queries/bugs")
	if status != 200 || !strings.Contains(body, "lib-bug") || strings.Contains(body, "hidden-bug") {
		t.Errorf("/alice/-/queries/bugs: %d\n%s", status, body)
	}
	if status, _ := browserGet(t, browser, inst.base()+"/alice/-/queries/nosuch"); status != 404 {
		t.Errorf("/alice/-/queries/nosuch: %d, want 404", status)
	}
	if status, _ := browserGet(t, browser, inst.base()+"/bob/-/queries/all"); status != 404 {
		t.Errorf("alice reached bob's query page: %d", status)
	}
	if _, body := inst.get(t, "/alice/-/queries/bugs"); strings.Contains(body, "lib-bug") {
		t.Error("an anonymous visitor reached a saved query page")
	}
}
