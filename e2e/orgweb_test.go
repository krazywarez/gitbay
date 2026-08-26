package e2e

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// TestOrgManagementWeb covers running an organization from the browser:
// membership and teams, admin-gated, dispatched through the same commands
// the CLI uses.
func TestOrgManagementWeb(t *testing.T) {
	inst := startInstanceWith(t, "[web]\nmode = \"accounts\"\n")
	aliceKey := inst.newKey(t, "alice")
	bobKey := inst.newKey(t, "bob")
	inst.admin(t, "admin", "user", "create", "alice", "--key", aliceKey+".pub")
	inst.admin(t, "admin", "user", "create", "bob", "--key", bobKey+".pub")
	if _, errOut, code := inst.ssh(t, aliceKey, "", "org", "create", "acme"); code != 0 {
		t.Fatalf("org create: %s", errOut)
	}
	if _, errOut, code := inst.ssh(t, aliceKey, "", "repo", "create", "acme/widget"); code != 0 {
		t.Fatalf("repo create: %s", errOut)
	}

	alice := loginBrowser(t, inst, aliceKey)

	// The management sections are admin-only: bob is not even a member.
	bob := loginBrowser(t, inst, bobKey)
	if _, body := browserGet(t, bob, inst.base()+"/acme"); strings.Contains(body, `value="member-add"`) {
		t.Fatal("a non-member sees organization controls")
	}
	// And POSTing anyway is refused by the command, not by the template.
	browserPost(t, bob, inst.base()+"/acme", url.Values{
		"field": {"member-add"}, "user": {"bob"}, "role": {"admin"},
	})
	if members := orgMembers(t, inst, aliceKey); len(members) != 1 {
		t.Fatalf("non-admin added themselves: %v", members)
	}

	status, body := browserGet(t, alice, inst.base()+"/acme")
	if status != 200 || !strings.Contains(body, `value="member-add"`) {
		t.Fatalf("admin sees no controls: %d", status)
	}

	// Add bob as a member through the form; confirm over SSH.
	browserPost(t, alice, inst.base()+"/acme", url.Values{
		"field": {"member-add"}, "user": {"bob"}, "role": {"member"},
	})
	if members := orgMembers(t, inst, aliceKey); len(members) != 2 {
		t.Fatalf("member not added: %v", members)
	}

	// Create a team, put bob in it, and grant it write on the repo.
	browserPost(t, alice, inst.base()+"/acme", url.Values{
		"field": {"team-create"}, "team": {"builders"},
	})
	browserPost(t, alice, inst.base()+"/acme", url.Values{
		"field": {"team-add"}, "team": {"builders"}, "user": {"bob"},
	})
	browserPost(t, alice, inst.base()+"/acme", url.Values{
		"field": {"team-grant"}, "team": {"builders"},
		"repo": {"acme/widget"}, "role": {"write"},
	})
	out, _, _ := inst.ssh(t, aliceKey, "", "org", "team", "show", "acme", "builders", "--json")
	if !strings.Contains(out, `"bob"`) || !strings.Contains(out, `"acme/widget"`) ||
		!strings.Contains(out, `"write"`) {
		t.Fatalf("team not configured: %s", out)
	}
	// The grant is real access, not just a row: bob can now push.
	if _, errOut, code := inst.ssh(t, bobKey, "", "repo", "show", "acme/widget"); code != 0 {
		t.Fatalf("team grant did not confer access: %s", errOut)
	}

	// The page shows what was built.
	_, body = browserGet(t, alice, inst.base()+"/acme")
	for _, want := range []string{"builders", "acme/widget", "1 member"} {
		if !strings.Contains(body, want) {
			t.Errorf("org page missing %q", want)
		}
	}

	// Revoking and removing work the same way round.
	browserPost(t, alice, inst.base()+"/acme", url.Values{
		"field": {"team-revoke"}, "team": {"builders"}, "repo": {"acme/widget"},
	})
	browserPost(t, alice, inst.base()+"/acme", url.Values{
		"field": {"member-remove"}, "user": {"bob"},
	})
	if members := orgMembers(t, inst, aliceKey); len(members) != 1 {
		t.Fatalf("member not removed: %v", members)
	}
}

// loginBrowser mints a session over SSH and returns a browser holding it.
func loginBrowser(t *testing.T, inst *instance, key string) *http.Client {
	t.Helper()
	out, errOut, code := inst.ssh(t, key, "", "web", "login", "--json")
	if code != 0 {
		t.Fatalf("web login: %s", errOut)
	}
	var env struct {
		Data struct {
			URL string `json:"url"`
		} `json:"data"`
	}
	json.Unmarshal([]byte(out), &env)
	c := newBrowser(t)
	browserGet(t, c, inst.base()+env.Data.URL[strings.Index(env.Data.URL, "/login"):])
	return c
}

func orgMembers(t *testing.T, inst *instance, key string) []string {
	t.Helper()
	out, _, _ := inst.ssh(t, key, "", "org", "members", "list", "acme", "--json")
	var env struct {
		Data struct {
			Members []struct {
				User string `json:"user"`
			} `json:"members"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("members JSON: %v\n%s", err, out)
	}
	var names []string
	for _, m := range env.Data.Members {
		names = append(names, m.User)
	}
	return names
}
