package e2e

import (
	"encoding/json"
	"net/url"
	"strings"
	"testing"
)

// Web writes dispatch the command the CLI runs, so a rule the command
// layer enforces holds from a browser too. Repo create used to call the
// store directly and skip the per-account quota; issue comments skipped
// the archived check (#93).
func TestWebWritesGoThroughRegistry(t *testing.T) {
	inst := startInstanceWith(t, "[web]\nmode = \"accounts\"\n[limits]\nmax_repos_per_user = 1\n")
	aliceKey := inst.newKey(t, "alice")
	inst.admin(t, "admin", "user", "create", "alice",
		"--key", aliceKey+".pub", "--email", "alice@example.test", "--verified")
	if _, errOut, code := inst.ssh(t, aliceKey, "", "repo", "create", "alice/first"); code != 0 {
		t.Fatalf("repo create: %s", errOut)
	}
	if _, errOut, code := inst.ssh(t, aliceKey, "", "issue", "create", "alice/first", "--title", "one"); code != 0 {
		t.Fatalf("issue create: %s", errOut)
	}

	out, errOut, code := inst.ssh(t, aliceKey, "", "web", "login", "--json")
	if code != 0 {
		t.Fatalf("web login: %s", errOut)
	}
	var env struct {
		Data struct {
			URL string `json:"url"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("web login JSON: %v\n%s", err, out)
	}
	browser := newBrowser(t)
	if status, _ := browserGet(t, browser, inst.base()+env.Data.URL[strings.Index(env.Data.URL, "/login"):]); status != 200 {
		t.Fatalf("login: %d", status)
	}

	// The quota holds on the web: the form comes back with the refusal and
	// no repository exists.
	status, body := browserPost(t, browser, inst.base()+"/new", url.Values{"name": {"second"}, "visibility": {"public"}})
	if status != 200 || !strings.Contains(body, "role=\"alert\"") {
		t.Fatalf("second repo over quota was not refused on the form: %d\n%s", status, body)
	}
	if _, _, code := inst.ssh(t, aliceKey, "", "repo", "show", "alice/second"); code != 3 {
		t.Fatalf("repo created past the quota from the web: exit %d, want 3", code)
	}

	// A form action on an issue that does not exist is the 404 page, not a
	// redirect carrying a message (#106).
	if status, _ := browserPost(t, browser, inst.base()+"/alice/first/issues/999/state", url.Values{"action": {"close"}}); status != 404 {
		t.Fatalf("closing a missing issue from the web: %d, want 404", status)
	}

	// An archived repository refuses a comment from the web as it does
	// over ssh.
	if _, errOut, code := inst.ssh(t, aliceKey, "", "repo", "archive", "alice/first"); code != 0 {
		t.Fatalf("repo archive: %s", errOut)
	}
	status, _ = browserPost(t, browser, inst.base()+"/alice/first/issues/1/comment", url.Values{"body": {"late"}})
	if status/100 != 4 {
		t.Fatalf("comment on an archived repo from the web: %d, want 4xx", status)
	}
	show, _, _ := inst.ssh(t, aliceKey, "", "issue", "show", "alice/first", "1")
	if strings.Contains(show, "late") {
		t.Fatalf("archived repo took a web comment:\n%s", show)
	}
}
