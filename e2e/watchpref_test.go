package e2e

import (
	"net/url"
	"strings"
	"testing"
)

// notifications settings watch on makes an account a recipient of every
// issue on the repositories it can write to, with no repo watch row;
// read access is not enough, and off returns it to the default. The
// account page carries the same switch (#194).
func TestWatchPreference(t *testing.T) {
	inst := startInstanceWith(t, "[web]\nmode = \"accounts\"\n")
	aliceKey := inst.newKey(t, "alice")
	bobKey := inst.newKey(t, "bob")
	carolKey := inst.newKey(t, "carol")
	daveKey := inst.newKey(t, "dave")
	inst.admin(t, "admin", "user", "create", "alice", "--key", aliceKey+".pub")
	inst.admin(t, "admin", "user", "create", "bob", "--key", bobKey+".pub")
	inst.admin(t, "admin", "user", "create", "carol", "--key", carolKey+".pub")
	inst.admin(t, "admin", "user", "create", "dave", "--key", daveKey+".pub")
	if _, errOut, code := inst.ssh(t, aliceKey, "", "repo", "create", "alice/app"); code != 0 {
		t.Fatalf("repo create: %s", errOut)
	}
	if _, errOut, code := inst.ssh(t, aliceKey, "", "repo", "access", "grant", "alice/app", "bob", "write"); code != 0 {
		t.Fatalf("grant bob: %s", errOut)
	}
	if _, errOut, code := inst.ssh(t, aliceKey, "", "repo", "access", "grant", "alice/app", "carol", "read"); code != 0 {
		t.Fatalf("grant carol: %s", errOut)
	}

	out, _, code := inst.ssh(t, bobKey, "", "notifications", "settings", "show", "--json")
	if code != 0 || !strings.Contains(out, `"watch":false`) {
		t.Fatalf("settings show: %d %s", code, out)
	}
	if _, _, code := inst.ssh(t, bobKey, "", "notifications", "settings", "watch", "maybe"); code != 2 {
		t.Fatalf("bad value accepted: %d", code)
	}
	for _, key := range []string{bobKey, carolKey} {
		if out, _, code := inst.ssh(t, key, "", "notifications", "settings", "watch", "on", "--json"); code != 0 || !strings.Contains(out, `"watch":true`) {
			t.Fatalf("watch on: %d %s", code, out)
		}
	}

	if _, errOut, code := inst.ssh(t, daveKey, "", "issue", "create", "alice/app", "--title", "'first'"); code != 0 {
		t.Fatalf("issue create: %s", errOut)
	}
	if rows := notices(t, inst, bobKey); len(rows) != 1 || !strings.Contains(rows[0].Summary, "opened issue #1") {
		t.Fatalf("writer's inbox: %+v", rows)
	}
	if rows := notices(t, inst, carolKey); len(rows) != 0 {
		t.Fatalf("reader's inbox: %+v", rows)
	}
	if out, _, _ := inst.ssh(t, bobKey, "", "repo", "show", "alice/app", "--json"); strings.Contains(out, `"watch":"watching"`) {
		t.Fatalf("preference wrote a watch row: %s", out)
	}

	// The account page shows it on and turns it off.
	bob := inst.login(t, bobKey)
	set := inst.base() + "/settings"
	if _, body := browserGet(t, bob, set); !strings.Contains(body, `name="watch" value="on" checked`) {
		t.Fatalf("account page does not show watch on:\n%s", body)
	}
	if status, _ := browserPost(t, bob, set, url.Values{"field": {"notify-watch"}}); status != 200 {
		t.Fatalf("settings post: %d", status)
	}
	if out, _, _ := inst.ssh(t, bobKey, "", "notifications", "settings", "show", "--json"); !strings.Contains(out, `"watch":false`) {
		t.Fatalf("web toggle did not turn watch off: %s", out)
	}
	if _, errOut, code := inst.ssh(t, daveKey, "", "issue", "create", "alice/app", "--title", "'second'"); code != 0 {
		t.Fatalf("second issue: %s", errOut)
	}
	if rows := notices(t, inst, bobKey); len(rows) != 1 {
		t.Fatalf("inbox after watch off: %+v", rows)
	}
}
