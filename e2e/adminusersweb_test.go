package e2e

import (
	"net/url"
	"strings"
	"testing"
)

// /admin/users lists accounts and runs the account commands, which the
// web can reach now that nothing is held back from it (#234). Demote
// and disable carry the typed-name check.
func TestAdminUsersWeb(t *testing.T) {
	t.Parallel()
	inst := startInstanceWith(t, "[web]\nmode = \"accounts\"\n")
	rootKey := inst.newKey(t, "root")
	aliceKey := inst.newKey(t, "alice")
	inst.admin(t, "admin", "user", "create", "root", "--key", rootKey+".pub", "--admin",
		"--email", "root@example.test", "--verified")
	inst.admin(t, "admin", "user", "create", "alice", "--key", aliceKey+".pub",
		"--email", "alice@example.test", "--verified")

	// A non-admin sees neither the page nor a hint that it exists.
	if status, _ := browserGet(t, inst.login(t, aliceKey), inst.base()+"/admin/users"); status != 404 {
		t.Fatalf("non-admin reached the account list: %d", status)
	}

	root := inst.login(t, rootKey)
	page := inst.base() + "/admin/users"
	_, body := browserGet(t, root, page)
	for _, want := range []string{">alice<", ">root<", "Promote", "Disable"} {
		if !strings.Contains(body, want) {
			t.Fatalf("account list missing %q:\n%s", want, body)
		}
	}
	// The admin page links here.
	if _, admin := browserGet(t, root, inst.base()+"/admin"); !strings.Contains(admin, `href="/admin/users"`) {
		t.Fatalf("admin page does not link the account list:\n%s", admin)
	}

	post := func(v url.Values) string {
		t.Helper()
		status, body := browserPost(t, root, page, v)
		if status != 200 {
			t.Fatalf("post %v: %d", v, status)
		}
		return body
	}

	// Disable needs the typed name: the wrong one changes nothing.
	post(url.Values{"field": {"disable"}, "user": {"alice"}, "confirm": {"alicce"}})
	if out, _, _ := inst.ssh(t, rootKey, "", "admin", "user", "show", "alice", "--json"); !strings.Contains(out, `"state":"active"`) {
		t.Fatalf("a mistyped confirm still disabled the account: %s", out)
	}
	post(url.Values{"field": {"disable"}, "user": {"alice"}, "confirm": {"alice"}})
	if out, _, _ := inst.ssh(t, rootKey, "", "admin", "user", "show", "alice", "--json"); !strings.Contains(out, `"state":"disabled"`) {
		t.Fatalf("disable did not take: %s", out)
	}
	post(url.Values{"field": {"enable"}, "user": {"alice"}})
	post(url.Values{"field": {"promote"}, "user": {"alice"}})
	if out, _, _ := inst.ssh(t, rootKey, "", "admin", "user", "show", "alice", "--json"); !strings.Contains(out, `"admin":true`) {
		t.Fatalf("promote did not take: %s", out)
	}
	post(url.Values{"field": {"demote"}, "user": {"alice"}, "confirm": {"alice"}})
	if out, _, _ := inst.ssh(t, rootKey, "", "admin", "user", "show", "alice", "--json"); strings.Contains(out, `"admin":true`) {
		t.Fatalf("demote did not take: %s", out)
	}

	// The command's own refusals reach the page: the last admin stays.
	b := post(url.Values{"field": {"demote"}, "user": {"root"}, "confirm": {"root"}})
	if !strings.Contains(b, "admin") {
		t.Fatalf("no message after demoting the last admin:\n%s", b)
	}
	if out, _, _ := inst.ssh(t, rootKey, "", "admin", "user", "show", "root", "--json"); !strings.Contains(out, `"admin":true`) {
		t.Fatalf("the last admin was demoted: %s", out)
	}

	// The state filter narrows the list.
	if _, body := browserGet(t, root, page+"?state=admin"); strings.Contains(body, ">alice<") {
		t.Fatalf("the admin filter listed a non-admin:\n%s", body)
	}
}
