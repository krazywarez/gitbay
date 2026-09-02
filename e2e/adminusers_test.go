package e2e

import (
	"encoding/json"
	"strings"
	"testing"
)

type adminUserRow struct {
	Username string `json:"username"`
	State    string `json:"state"`
	Admin    bool   `json:"admin"`
	LastSeen string `json:"last_seen"`
}

func TestAdminUserListAndShow(t *testing.T) {
	inst := startInstanceWith(t, "[web]\nmode = \"accounts\"\n")
	adminKey := inst.newKey(t, "root")
	aliceKey := inst.newKey(t, "alice")
	bobKey := inst.newKey(t, "bob")
	inst.admin(t, "admin", "user", "create", "root", "--key", adminKey+".pub", "--admin")
	inst.admin(t, "admin", "user", "create", "alice", "--key", aliceKey+".pub",
		"--email", "alice@example.org", "--verified")
	inst.admin(t, "admin", "user", "create", "bob", "--key", bobKey+".pub")
	inst.admin(t, "admin", "user", "disable", "bob")

	if _, _, code := inst.ssh(t, aliceKey, "", "admin", "user", "list"); code != 4 {
		t.Fatalf("non-admin listed users: exit %d", code)
	}
	if _, _, code := inst.ssh(t, aliceKey, "", "admin", "user", "show", "bob"); code != 4 {
		t.Fatalf("non-admin showed a user: exit %d", code)
	}

	list := func(args ...string) []adminUserRow {
		t.Helper()
		out, errOut, code := inst.ssh(t, adminKey, "", append([]string{"admin", "user", "list", "--json"}, args...)...)
		if code != 0 {
			t.Fatalf("admin user list %v: exit %d\n%s", args, code, errOut)
		}
		var env struct {
			Data json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal([]byte(out), &env); err != nil {
			t.Fatalf("list envelope: %v\n%s", err, out)
		}
		var rows []adminUserRow
		if err := json.Unmarshal(env.Data, &rows); err != nil {
			// paged shape
			var paged struct {
				Items []adminUserRow `json:"items"`
				Next  string         `json:"next"`
			}
			if err := json.Unmarshal(env.Data, &paged); err != nil {
				t.Fatalf("list shape: %v\n%s", err, out)
			}
			return paged.Items
		}
		return rows
	}

	// gitbay-bot is seeded by the schema: it authors dependency issues.
	rows := list()
	if len(rows) != 4 || rows[0].Username != "alice" || rows[1].Username != "bob" ||
		rows[2].Username != "gitbay-bot" || rows[3].Username != "root" {
		t.Fatalf("list: %+v", rows)
	}
	if rows[1].State != "disabled" || rows[0].State != "active" || !rows[3].Admin || rows[0].Admin {
		t.Fatalf("states: %+v", rows)
	}
	if rows[0].LastSeen == "" {
		t.Fatal("alice authenticated above but has no last_seen")
	}
	if rows[1].LastSeen != "" {
		t.Fatalf("bob never authenticated but has last_seen %q", rows[1].LastSeen)
	}
	if rows := list("--state", "disabled"); len(rows) != 1 || rows[0].Username != "bob" {
		t.Fatalf("--state disabled: %+v", rows)
	}
	if rows := list("--state", "admin"); len(rows) != 1 || rows[0].Username != "root" {
		t.Fatalf("--state admin: %+v", rows)
	}
	if rows := list("--state", "active"); len(rows) != 3 {
		t.Fatalf("--state active: %+v", rows)
	}
	if _, _, code := inst.ssh(t, adminKey, "", "admin", "user", "list", "--state", "bogus"); code != 2 {
		t.Fatalf("bad --state accepted: exit %d", code)
	}

	// Pagination: two pages of usernames, keyset by username.
	out, _, _ := inst.ssh(t, adminKey, "", "admin", "user", "list", "--json", "--limit", "2")
	var env struct {
		Data struct {
			Items []adminUserRow `json:"items"`
			Next  string         `json:"next"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil || len(env.Data.Items) != 2 || env.Data.Next == "" {
		t.Fatalf("first page: %v\n%s", err, out)
	}
	if rows := list("--limit", "2", "--cursor", env.Data.Next); len(rows) != 2 ||
		rows[0].Username != "gitbay-bot" || rows[1].Username != "root" {
		t.Fatalf("second page: %+v", rows)
	}

	// Show: alice owns a repo, admins an org, has a verified email.
	if _, _, code := inst.ssh(t, aliceKey, "", "repo", "create", "alice/app"); code != 0 {
		t.Fatal("repo create failed")
	}
	if _, _, code := inst.ssh(t, aliceKey, "", "org", "create", "acme"); code != 0 {
		t.Fatal("org create failed")
	}
	out, errOut, code := inst.ssh(t, adminKey, "", "admin", "user", "show", "alice", "--json")
	if code != 0 {
		t.Fatalf("show: exit %d\n%s", code, errOut)
	}
	var show struct {
		Data struct {
			adminUserRow
			Keys []struct {
				Fingerprint string `json:"fingerprint"`
				Scope       string `json:"scope"`
				LastUsedAt  string `json:"last_used_at"`
			} `json:"keys"`
			Emails []struct {
				Address    string `json:"address"`
				Verified   bool   `json:"verified"`
				VerifiedBy string `json:"verified_by"`
			} `json:"emails"`
			Orgs []struct {
				Org  string `json:"org"`
				Role string `json:"role"`
			} `json:"orgs"`
			Repos       int64 `json:"repos"`
			WebSessions int64 `json:"web_sessions"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &show); err != nil {
		t.Fatalf("show envelope: %v\n%s", err, out)
	}
	d := show.Data
	if d.Username != "alice" || d.State != "active" || d.Repos != 1 || d.WebSessions != 0 {
		t.Fatalf("show summary: %+v", d)
	}
	if len(d.Keys) != 1 || !strings.HasPrefix(d.Keys[0].Fingerprint, "SHA256:") || d.Keys[0].Scope != "full" || d.Keys[0].LastUsedAt == "" {
		t.Fatalf("show keys: %+v", d.Keys)
	}
	if len(d.Emails) != 1 || d.Emails[0].Address != "alice@example.org" || !d.Emails[0].Verified || d.Emails[0].VerifiedBy != "admin" {
		t.Fatalf("show emails: %+v", d.Emails)
	}
	if len(d.Orgs) != 1 || d.Orgs[0].Org != "acme" || d.Orgs[0].Role != "admin" {
		t.Fatalf("show orgs: %+v", d.Orgs)
	}
	// A browser session counts once minted.
	inst.login(t, aliceKey)
	out, _, _ = inst.ssh(t, adminKey, "", "admin", "user", "show", "alice", "--json")
	if !strings.Contains(out, `"web_sessions":1`) {
		t.Fatalf("session not counted:\n%s", out)
	}

	if _, _, code := inst.ssh(t, adminKey, "", "admin", "user", "show", "nobody"); code != 3 {
		t.Fatalf("unknown user: exit %d", code)
	}
	// Plain output carries the same facts.
	if out, _, _ := inst.ssh(t, adminKey, "", "admin", "user", "show", "alice"); !strings.Contains(out, "alice\tactive") ||
		!strings.Contains(out, "acme\tadmin") || !strings.Contains(out, "verified by admin") {
		t.Fatalf("plain show:\n%s", out)
	}
}

func TestAdminPromoteDemote(t *testing.T) {
	inst := startInstance(t)
	rootKey := inst.newKey(t, "root")
	aliceKey := inst.newKey(t, "alice")
	bobKey := inst.newKey(t, "bob")
	inst.admin(t, "admin", "user", "create", "root", "--key", rootKey+".pub", "--admin")
	inst.admin(t, "admin", "user", "create", "alice", "--key", aliceKey+".pub")
	inst.admin(t, "admin", "user", "create", "bob", "--key", bobKey+".pub")
	inst.admin(t, "admin", "user", "disable", "bob")

	if _, _, code := inst.ssh(t, aliceKey, "", "admin", "user", "promote", "alice"); code != 4 {
		t.Fatalf("non-admin promoted: exit %d", code)
	}
	if _, _, code := inst.ssh(t, rootKey, "", "admin", "user", "promote", "nobody"); code != 3 {
		t.Fatalf("unknown user: exit %d", code)
	}
	if _, errOut, code := inst.ssh(t, rootKey, "", "admin", "user", "promote", "bob"); code != 2 || !strings.Contains(errOut, "disabled") {
		t.Fatalf("disabled account promoted: exit %d %s", code, errOut)
	}
	// The only admin cannot step down.
	if _, errOut, code := inst.ssh(t, rootKey, "", "admin", "user", "demote", "root"); code != 2 || !strings.Contains(errOut, "only instance admin") {
		t.Fatalf("last admin demoted: exit %d %s", code, errOut)
	}
	if _, _, code := inst.ssh(t, rootKey, "", "admin", "user", "promote", "alice"); code != 0 {
		t.Fatal("promote failed")
	}
	if _, _, code := inst.ssh(t, rootKey, "", "admin", "user", "promote", "alice"); code != 2 {
		t.Fatal("promoting an admin should be a usage error")
	}
	if out, _, code := inst.ssh(t, aliceKey, "", "audit"); code != 0 || !strings.Contains(out, "cmd admin user promote") {
		t.Fatalf("promoted account cannot read the audit log, or the promotion is not in it: exit %d\n%s", code, out)
	}
	// With two admins, either may demote the other; then the survivor is stuck.
	if _, _, code := inst.ssh(t, aliceKey, "", "admin", "user", "demote", "root"); code != 0 {
		t.Fatal("demote failed")
	}
	if _, _, code := inst.ssh(t, rootKey, "", "audit"); code != 4 {
		t.Fatal("demoted account still admin")
	}
	if _, _, code := inst.ssh(t, aliceKey, "", "admin", "user", "demote", "alice"); code != 2 {
		t.Fatal("last admin demoted")
	}
	// Host-local recovery: the operator restores root without an admin key.
	if out := inst.forgedAdminErr(t, "admin", "user", "demote", "alice"); !strings.Contains(out, "only instance admin") {
		t.Fatalf("host demote of last admin: %s", out)
	}
	inst.admin(t, "admin", "user", "promote", "root")
	if _, _, code := inst.ssh(t, rootKey, "", "audit"); code != 0 {
		t.Fatal("host promote did not take")
	}
	if out := inst.admin(t, "admin", "audit"); !strings.Contains(out, "admin user.promoted") {
		t.Fatalf("host promote not audited:\n%s", out)
	}
}
