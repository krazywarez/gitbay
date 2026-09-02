package e2e

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

func TestAdminRepoModeration(t *testing.T) {
	inst := startInstance(t)
	rootKey := inst.newKey(t, "root")
	aliceKey := inst.newKey(t, "alice")
	inst.admin(t, "admin", "user", "create", "root", "--key", rootKey+".pub", "--admin")
	inst.admin(t, "admin", "user", "create", "alice", "--key", aliceKey+".pub")
	for _, args := range [][]string{{"repo", "create", "alice/app"}, {"repo", "create", "alice/secret", "--private"}} {
		if _, _, code := inst.ssh(t, aliceKey, "", args...); code != 0 {
			t.Fatalf("%v failed", args)
		}
	}
	// A push, so last_push has something to report.
	work := t.TempDir()
	env := inst.gitEnv(aliceKey)
	mustGit(t, work, env, "clone", inst.sshURL("alice/app"), "w")
	dir := filepath.Join(work, "w")
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a\n"), 0o644)
	mustGit(t, dir, env, "checkout", "-q", "-b", "main")
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, env, "commit", "-q", "-m", "a")
	mustGit(t, dir, env, "push", "-q", "origin", "main")

	// Instance admin carries no read right: the private repo still 404s.
	if _, _, code := inst.ssh(t, rootKey, "", "repo", "show", "alice/secret"); code != 3 {
		t.Fatalf("admin read a private repo: exit %d", code)
	}
	if _, _, code := inst.ssh(t, aliceKey, "", "admin", "repo", "list"); code != 4 {
		t.Fatal("non-admin listed repos")
	}

	type row struct {
		Path       string `json:"path"`
		Visibility string `json:"visibility"`
		Archived   bool   `json:"archived"`
		LastPush   string `json:"last_push"`
		Bytes      int64  `json:"bytes"`
	}
	list := func(args ...string) []row {
		t.Helper()
		out, errOut, code := inst.ssh(t, rootKey, "", append([]string{"admin", "repo", "list", "--json"}, args...)...)
		if code != 0 {
			t.Fatalf("admin repo list %v: exit %d %s", args, code, errOut)
		}
		var env struct {
			Data []row `json:"data"`
		}
		if err := json.Unmarshal([]byte(out), &env); err != nil {
			t.Fatalf("list: %v\n%s", err, out)
		}
		return env.Data
	}
	rows := list()
	if len(rows) != 2 || rows[0].Path != "alice/app" || rows[1].Path != "alice/secret" || rows[1].Visibility != "private" {
		t.Fatalf("list: %+v", rows)
	}
	if rows[0].LastPush == "" || rows[1].LastPush != "" || rows[0].Bytes == 0 {
		t.Fatalf("push and size facts: %+v", rows)
	}
	if rows := list("--visibility", "private"); len(rows) != 1 || rows[0].Path != "alice/secret" {
		t.Fatalf("--visibility: %+v", rows)
	}
	if rows := list("--owner", "root"); len(rows) != 0 {
		t.Fatalf("--owner root: %+v", rows)
	}

	// Archive, then visibility: the private repo becomes readable to
	// everyone once public, admin included.
	if _, _, code := inst.ssh(t, rootKey, "", "admin", "repo", "archive", "alice/app"); code != 0 {
		t.Fatal("admin archive failed")
	}
	if rows := list(); !rows[0].Archived {
		t.Fatalf("not archived: %+v", rows)
	}
	if _, _, code := inst.ssh(t, rootKey, "", "admin", "repo", "unarchive", "alice/app"); code != 0 {
		t.Fatal("admin unarchive failed")
	}
	if _, _, code := inst.ssh(t, rootKey, "", "admin", "repo", "visibility", "alice/secret", "public"); code != 0 {
		t.Fatal("admin visibility failed")
	}
	if _, _, code := inst.ssh(t, rootKey, "", "repo", "show", "alice/secret"); code != 0 {
		t.Fatal("repo still hidden after going public")
	}

	// Delete wants the typed confirmation and then removes it from the
	// owner's view too.
	if _, _, code := inst.ssh(t, rootKey, "", "admin", "repo", "delete", "alice/app"); code != 2 {
		t.Fatal("delete without --yes accepted")
	}
	if _, _, code := inst.ssh(t, rootKey, "", "admin", "repo", "delete", "alice/app", "--yes"); code != 0 {
		t.Fatal("admin delete failed")
	}
	if out, _, _ := inst.ssh(t, aliceKey, "", "repo", "list"); strings.Contains(out, "alice/app") {
		t.Fatalf("deleted repo still listed:\n%s", out)
	}
	if _, _, code := inst.ssh(t, rootKey, "", "admin", "repo", "delete", "nobody/none", "--yes"); code != 3 {
		t.Fatal("unknown repo should be not found")
	}

	// Every override is in the audit log under its own action, on top of
	// the generic cmd row.
	out, _, _ := inst.ssh(t, rootKey, "", "audit", "--json")
	for _, want := range []string{"admin repo.archive", "admin repo.unarchive", "admin repo.visibility", "admin repo.delete"} {
		if !strings.Contains(out, want) {
			t.Fatalf("audit lacks %q:\n%s", want, out)
		}
	}
}

// The host-local admin commands dispatch into the registry, so the same
// commands work in an admin's SSH session and audit rows say which path
// ran them.
func TestAdminHostAndSSHAreOneSurface(t *testing.T) {
	inst := startInstance(t)
	rootKey := inst.newKey(t, "root")
	aliceKey := inst.newKey(t, "alice")
	carolKey := inst.newKey(t, "carol")
	inst.admin(t, "admin", "user", "create", "root", "--key", rootKey+".pub", "--admin")
	inst.admin(t, "admin", "user", "create", "alice", "--key", aliceKey+".pub")

	pub, err := os.ReadFile(carolKey + ".pub")
	if err != nil {
		t.Fatal(err)
	}
	out, errOut, code := inst.ssh(t, rootKey, string(pub), "admin", "user", "create", "carol",
		"--email", "carol@example.test", "--verified", "--key", "-")
	if code != 0 || !strings.Contains(out, "created user carol") || !strings.Contains(out, "key SHA256:") {
		t.Fatalf("ssh user create: exit %d\n%s%s", code, out, errOut)
	}
	if _, _, code := inst.ssh(t, carolKey, "", "whoami"); code != 0 {
		t.Fatal("created account cannot authenticate")
	}
	if _, errOut, code := inst.ssh(t, rootKey, "", "admin", "user", "create", "alice"); code != 2 || !strings.Contains(errOut, "taken") {
		t.Fatalf("duplicate create: exit %d %s", code, errOut)
	}
	for _, args := range [][]string{{"admin", "stats"}, {"admin", "user", "disable", "carol"}, {"admin", "invite", "--email", "x@example.test"}} {
		if _, _, code := inst.ssh(t, aliceKey, "", args...); code != 4 {
			t.Fatalf("non-admin ran %v: exit %d", args, code)
		}
	}
	if _, _, code := inst.ssh(t, rootKey, "", "admin", "user", "disable", "carol"); code != 0 {
		t.Fatal("ssh disable failed")
	}
	if _, _, code := inst.ssh(t, carolKey, "", "whoami"); code != 4 {
		t.Fatal("disabled account still authenticates")
	}
	inst.admin(t, "admin", "user", "enable", "carol")
	if _, _, code := inst.ssh(t, carolKey, "", "whoami"); code != 0 {
		t.Fatal("host enable did not take")
	}
	if out, _, code := inst.ssh(t, rootKey, "", "admin", "stats", "--json"); code != 0 || !strings.Contains(out, `"users":`) {
		t.Fatalf("ssh stats: exit %d\n%s", code, out)
	}
	// No SMTP: the invite code comes back on stdout instead of by mail.
	if out, _, code := inst.ssh(t, rootKey, "", "admin", "invite", "--email", "dave@example.test", "--json"); code != 0 || !strings.Contains(out, `"code":"`) {
		t.Fatalf("ssh invite: exit %d\n%s", code, out)
	}
	if _, errOut, code := inst.ssh(t, rootKey, "", "admin", "email", "verify", "carol", "nope@example.test"); code != 3 || !strings.Contains(errOut, "no address") {
		t.Fatalf("verify unknown address: exit %d %s", code, errOut)
	}
	if _, _, code := inst.ssh(t, rootKey, "", "admin", "user", "delete", "carol", "--yes"); code != 0 {
		t.Fatal("ssh delete failed")
	}
	if _, _, code := inst.ssh(t, rootKey, "", "admin", "user", "delete", "root", "--yes"); code != 2 {
		t.Fatal("deleted own account")
	}

	// Both paths audit under the same action names; the row says which
	// credential ran it.
	audit := inst.admin(t, "admin", "audit")
	for _, want := range []string{`"source":"host"`, `"source":"SHA256:`, "admin user.created", "admin user.disabled", "admin user.enabled", "admin user.deleted"} {
		if !strings.Contains(audit, want) {
			t.Fatalf("audit lacks %q:\n%s", want, audit)
		}
	}
}

func TestAuditFilters(t *testing.T) {
	inst := startInstance(t)
	rootKey := inst.newKey(t, "root")
	aliceKey := inst.newKey(t, "alice")
	bobKey := inst.newKey(t, "bob")
	inst.admin(t, "admin", "user", "create", "root", "--key", rootKey+".pub", "--admin")
	inst.admin(t, "admin", "user", "create", "alice", "--key", aliceKey+".pub")
	inst.admin(t, "admin", "user", "create", "bob", "--key", bobKey+".pub")
	for _, c := range [][]string{{aliceKey, "alice/app"}, {bobKey, "bob/app"}} {
		if _, _, code := inst.ssh(t, c[0], "", "repo", "create", c[1]); code != 0 {
			t.Fatalf("repo create %s failed", c[1])
		}
	}
	audit := func(args ...string) string {
		t.Helper()
		out, errOut, code := inst.ssh(t, rootKey, "", append([]string{"audit"}, args...)...)
		if code != 0 {
			t.Fatalf("audit %v: exit %d %s", args, code, errOut)
		}
		return out
	}
	if out := audit("--actor", "alice"); !strings.Contains(out, "alice/app") || strings.Contains(out, "bob/app") || strings.Contains(out, "user.created") {
		t.Fatalf("--actor alice:\n%s", out)
	}
	if out := audit("--actor", "-"); !strings.Contains(out, "admin user.created") || strings.Contains(out, "repo create") {
		t.Fatalf("--actor -:\n%s", out)
	}
	if out := audit("--action", "'cmd repo'"); strings.Count(out, "\n") != 2 || strings.Contains(out, "user.created") {
		t.Fatalf("--action prefix:\n%s", out)
	}
	if out := audit("--action", "'cmd repo'", "--limit", "1"); strings.Count(out, "\n") != 1 {
		t.Fatalf("--limit with filter:\n%s", out)
	}
	if out := audit("--since", "1h"); !strings.Contains(out, "alice/app") {
		t.Fatalf("--since 1h:\n%s", out)
	}
	if out := audit("--since", "2099-01-01"); strings.TrimSpace(out) != "" {
		t.Fatalf("--since in the future returned rows:\n%s", out)
	}
	if _, _, code := inst.ssh(t, rootKey, "", "audit", "--since", "yesterday"); code != 2 {
		t.Fatal("bad --since accepted")
	}
	if _, _, code := inst.ssh(t, rootKey, "", "audit", "--actor"); code != 2 {
		t.Fatal("dangling flag accepted")
	}
	// The host-local command takes the same flags and --json.
	if out := inst.admin(t, "admin", "audit", "--actor", "bob", "--json"); !strings.Contains(out, `"protocol_version"`) ||
		!strings.Contains(out, "bob/app") || strings.Contains(out, "alice/app") {
		t.Fatalf("host audit --json --actor:\n%s", out)
	}
}

func TestAdminQueuesDashboard(t *testing.T) {
	inst := startInstanceWith(t, "[web]\nmode = \"accounts\"\n[webhooks]\nallow_local = true\n")
	rootKey := inst.newKey(t, "root")
	aliceKey := inst.newKey(t, "alice")
	inst.admin(t, "admin", "user", "create", "root", "--key", rootKey+".pub", "--admin")
	inst.admin(t, "admin", "user", "create", "alice", "--key", aliceKey+".pub")
	if _, _, code := inst.ssh(t, aliceKey, "", "repo", "create", "alice/app"); code != 0 {
		t.Fatal("repo create failed")
	}
	// A webhook whose receiver keeps failing, and a CI job with no runner:
	// one delivery retrying, one build pending.
	hook := startHookReceiver(t)
	hook.failNext = 100
	if _, errOut, code := inst.ssh(t, aliceKey, "", "webhook", "add", "alice/app", "http://"+hook.addr+"/hook"); code != 0 {
		t.Fatalf("webhook add: %s", errOut)
	}
	work := t.TempDir()
	env := inst.gitEnv(aliceKey)
	mustGit(t, work, env, "clone", inst.sshURL("alice/app"), "w")
	dir := filepath.Join(work, "w")
	os.MkdirAll(filepath.Join(dir, ".gitbay"), 0o755)
	os.WriteFile(filepath.Join(dir, ".gitbay", "ci.yml"), []byte("jobs:\n  ok:\n    steps:\n      - echo fine\n"), 0o644)
	mustGit(t, dir, env, "checkout", "-q", "-b", "main")
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, env, "commit", "-q", "-m", "ci")
	mustGit(t, dir, env, "push", "-q", "origin", "main")

	type queues struct {
		Webhooks struct {
			Pending  int64 `json:"pending"`
			Retrying int64 `json:"retrying"`
			Items    []struct {
				Repo      string `json:"repo"`
				Attempts  int64  `json:"attempts"`
				LastError string `json:"last_error"`
			} `json:"items"`
		} `json:"webhooks"`
		Builds struct {
			Pending       int64  `json:"pending"`
			OldestPending string `json:"oldest_pending"`
		} `json:"builds"`
		Mail struct {
			Pending int64 `json:"pending"`
		} `json:"mail"`
	}
	dashboard := func(key string) (*queues, string) {
		t.Helper()
		out, errOut, code := inst.ssh(t, key, "", "dashboard", "--json")
		if code != 0 {
			t.Fatalf("dashboard: exit %d %s", code, errOut)
		}
		var env struct {
			Data struct {
				Queues *queues `json:"queues"`
			} `json:"data"`
		}
		if err := json.Unmarshal([]byte(out), &env); err != nil {
			t.Fatalf("dashboard json: %v\n%s", err, out)
		}
		return env.Data.Queues, out
	}
	if q, out := dashboard(aliceKey); q != nil {
		t.Fatalf("non-admin dashboard carries queues:\n%s", out)
	}
	var q *queues
	deadline := time.Now().Add(20 * time.Second)
	for {
		q, _ = dashboard(rootKey)
		if q != nil && q.Webhooks.Retrying >= 1 && q.Builds.Pending >= 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("queues never showed the retrying delivery and pending build: %+v", q)
		}
		time.Sleep(200 * time.Millisecond)
	}
	if q.Builds.OldestPending == "" || q.Mail.Pending != 0 {
		t.Fatalf("queue facts: %+v", q)
	}
	if len(q.Webhooks.Items) == 0 || q.Webhooks.Items[0].Repo != "alice/app" || q.Webhooks.Items[0].Attempts == 0 || q.Webhooks.Items[0].LastError == "" {
		t.Fatalf("retrying item: %+v", q.Webhooks.Items)
	}

	// The web page dispatches the same read; non-admins get a 404 and no
	// rail link.
	alice := inst.login(t, aliceKey)
	if status, body := browserGet(t, alice, inst.base()+"/admin"); status != 404 || strings.Contains(body, "Webhook deliveries") {
		t.Fatalf("non-admin /admin: %d", status)
	}
	if _, body := browserGet(t, alice, inst.base()+"/"); strings.Contains(body, `href="/admin"`) {
		t.Fatal("non-admin rail links to /admin")
	}
	root := inst.login(t, rootKey)
	status, body := browserGet(t, root, inst.base()+"/admin")
	if status != 200 || !strings.Contains(body, "Webhook deliveries") || !strings.Contains(body, "alice/app") ||
		!strings.Contains(body, "retrying") || !strings.Contains(body, "1 pending") {
		t.Fatalf("/admin: %d\n%s", status, body)
	}
	if _, body := browserGet(t, root, inst.base()+"/"); !strings.Contains(body, `href="/admin"`) {
		t.Fatal("admin rail lacks /admin")
	}
}
