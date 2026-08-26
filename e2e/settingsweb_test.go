package e2e

import (
	"net/url"
	"strings"
	"testing"
)

// TestRepoSettingsWeb drives the settings page: each control runs the
// command the CLI runs, so repo show and settings show are the check.
func TestRepoSettingsWeb(t *testing.T) {
	inst := startInstanceWith(t, "[web]\nmode = \"accounts\"\n")
	aliceKey := inst.newKey(t, "alice")
	bobKey := inst.newKey(t, "bob")
	inst.admin(t, "admin", "user", "create", "alice",
		"--key", aliceKey+".pub", "--email", "alice@example.test", "--verified")
	inst.admin(t, "admin", "user", "create", "bob", "--key", bobKey+".pub")
	if _, errOut, code := inst.ssh(t, aliceKey, "", "repo", "create", "alice/app"); code != 0 {
		t.Fatalf("repo create: %s", errOut)
	}

	alice := inst.login(t, aliceKey)
	set := inst.base() + "/alice/app/settings"

	// Only admins reach the page, and only they see the tab.
	if _, body := browserGet(t, alice, inst.base()+"/alice/app"); !strings.Contains(body, "/alice/app/settings") {
		t.Fatalf("no settings tab for the owner:\n%s", body)
	}
	if status, _ := browserGet(t, inst.login(t, bobKey), set); status != 403 && status != 404 {
		t.Fatalf("reader reached settings: %d", status)
	}

	post := func(v url.Values) string {
		t.Helper()
		status, body := browserPost(t, alice, set, v)
		if status != 200 {
			t.Fatalf("settings post %v: %d", v, status)
		}
		return body
	}

	post(url.Values{"field": {"description"}, "description": {"a fine tool"}})
	post(url.Values{"field": {"website"}, "website": {"https://tool.example"}})
	post(url.Values{"field": {"topics"}, "add": {"cli forge"}})
	post(url.Values{"field": {"require-checks"}, "require-checks": {"on"}})
	post(url.Values{"field": {"require-approvals"}, "approvals": {"2"}})
	post(url.Values{"field": {"protect"}, "branch": {"main"}})

	out, _, _ := inst.ssh(t, aliceKey, "", "repo", "show", "alice/app", "--json")
	for _, want := range []string{"a fine tool", "https://tool.example", `"cli"`, `"forge"`} {
		if !strings.Contains(out, want) {
			t.Fatalf("repo show missing %q:\n%s", want, out)
		}
	}
	out, _, _ = inst.ssh(t, aliceKey, "", "repo", "settings", "show", "alice/app", "--json")
	for _, want := range []string{`"require_checks":true`, `"require_approvals":2`, `"main"`} {
		if !strings.Contains(out, want) {
			t.Fatalf("settings show missing %q:\n%s", want, out)
		}
	}

	// Visibility is a new command; the web form drives it both ways.
	post(url.Values{"field": {"visibility"}, "visibility": {"private"}})
	if out, _, _ := inst.ssh(t, aliceKey, "", "repo", "show", "alice/app", "--json"); !strings.Contains(out, `"visibility":"private"`) {
		t.Fatalf("not private:\n%s", out)
	}
	// A private repo disappears from anonymous surfaces.
	if status, _ := inst.get(t, "/alice/app"); status != 404 {
		t.Fatalf("private repo still public: %d", status)
	}
	post(url.Values{"field": {"visibility"}, "visibility": {"public"}})
	if status, _ := inst.get(t, "/alice/app"); status != 200 {
		t.Fatalf("public repo not restored: %d", status)
	}

	// Archiving is reversible from the page; unchecking the box unarchives.
	post(url.Values{"field": {"archive"}, "archive": {"on"}})
	if out, _, _ := inst.ssh(t, aliceKey, "", "repo", "show", "alice/app", "--json"); !strings.Contains(out, `"archived":true`) {
		t.Fatalf("not archived:\n%s", out)
	}
	post(url.Values{"field": {"archive"}})
	if out, _, _ := inst.ssh(t, aliceKey, "", "repo", "show", "alice/app", "--json"); strings.Contains(out, `"archived":true`) {
		t.Fatalf("still archived:\n%s", out)
	}

	// Unprotecting works, and a refusal surfaces the command's message.
	post(url.Values{"field": {"unprotect"}, "branch": {"main"}})
	if out, _, _ := inst.ssh(t, aliceKey, "", "repo", "settings", "show", "alice/app", "--json"); strings.Contains(out, `"protected_branches"`) {
		t.Fatalf("branch still protected:\n%s", out)
	}
	if body := post(url.Values{"field": {"website"}, "website": {"javascript:alert(1)"}}); !strings.Contains(body, `class="error"`) {
		t.Fatalf("bad website accepted:\n%s", body)
	}
}
