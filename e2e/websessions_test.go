package e2e

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// A browser session can be listed and ended from SSH, one at a time or
// all at once, and only its owner sees it.
func TestWebSessionsListRevoke(t *testing.T) {
	inst := startInstanceWith(t, "[web]\nmode = \"accounts\"\n")
	aliceKey := inst.newKey(t, "alice")
	bobKey := inst.newKey(t, "bob")
	inst.admin(t, "admin", "user", "create", "alice", "--key", aliceKey+".pub")
	inst.admin(t, "admin", "user", "create", "bob", "--key", bobKey+".pub")

	if out, _, code := inst.ssh(t, aliceKey, "", "web", "sessions", "list", "--json"); code != 0 || !strings.Contains(out, `"data":[]`) {
		t.Fatalf("no sessions yet: exit %d %s", code, out)
	}
	first := inst.login(t, aliceKey)
	second := inst.login(t, aliceKey)
	list := func() []struct {
		ID string `json:"id"`
	} {
		t.Helper()
		out, errOut, code := inst.ssh(t, aliceKey, "", "web", "sessions", "list", "--json")
		if code != 0 {
			t.Fatalf("list: %s", errOut)
		}
		var env struct {
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		if err := json.Unmarshal([]byte(out), &env); err != nil {
			t.Fatalf("list json: %v\n%s", err, out)
		}
		return env.Data
	}
	sessions := list()
	if len(sessions) != 2 || len(sessions[0].ID) != 12 {
		t.Fatalf("two sessions expected: %+v", sessions)
	}
	// Bob sees none of them, and cannot revoke one by id.
	if out, _, _ := inst.ssh(t, bobKey, "", "web", "sessions", "list", "--json"); !strings.Contains(out, `"data":[]`) {
		t.Fatalf("bob sees alice's sessions:\n%s", out)
	}
	if _, _, code := inst.ssh(t, bobKey, "", "web", "sessions", "revoke", sessions[0].ID); code != 3 {
		t.Fatalf("bob revoked alice's session: exit %d", code)
	}
	// Both browsers work; revoking the newest logs that one out.
	// The client follows the logged-out redirect to /login, so the page
	// body tells the two apart, not the status.
	loggedIn := func(c *http.Client) bool {
		_, body := browserGet(t, c, inst.base()+"/settings")
		return strings.Contains(body, "SSH keys")
	}
	if !loggedIn(first) || !loggedIn(second) {
		t.Fatal("both browsers should be logged in")
	}
	if out, _, code := inst.ssh(t, aliceKey, "", "web", "sessions", "revoke", sessions[0].ID); code != 0 || !strings.Contains(out, "revoked browser session") {
		t.Fatalf("revoke: exit %d %s", code, out)
	}
	if got := list(); len(got) != 1 {
		t.Fatalf("one session left expected: %+v", got)
	}
	okCount := 0
	for _, c := range []*http.Client{first, second} {
		if loggedIn(c) {
			okCount++
		}
	}
	if okCount != 1 {
		t.Fatalf("exactly one browser should still be logged in, got %d", okCount)
	}
	if out, _, code := inst.ssh(t, aliceKey, "", "web", "sessions", "revoke", "--all"); code != 0 || !strings.Contains(out, "revoked 1 browser sessions") {
		t.Fatalf("revoke --all: exit %d %s", code, out)
	}
	if loggedIn(first) || loggedIn(second) {
		t.Fatal("a browser is still logged in after revoke --all")
	}
	if _, _, code := inst.ssh(t, aliceKey, "", "web", "sessions", "revoke", "abcdefabcdef"); code != 3 {
		t.Fatal("unknown id accepted")
	}
}
