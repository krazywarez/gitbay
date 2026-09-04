package e2e

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
)

// notices reads a user's inbox. The instance under test has no SMTP, so
// this also asserts the inbox is filed independently of mail.
func notices(t *testing.T, inst *instance, key string, args ...string) []struct {
	ID      int64  `json:"id"`
	Repo    string `json:"repo"`
	Kind    string `json:"kind"`
	Actor   string `json:"actor"`
	Summary string `json:"summary"`
	Path    string `json:"path"`
	ReadAt  string `json:"read_at"`
} {
	t.Helper()
	out, errOut, code := inst.ssh(t, key, "", append([]string{"notifications", "list"}, append(args, "--json")...)...)
	if code != 0 {
		t.Fatalf("notifications list: %s", errOut)
	}
	var env struct {
		Data []struct {
			ID      int64  `json:"id"`
			Repo    string `json:"repo"`
			Kind    string `json:"kind"`
			Actor   string `json:"actor"`
			Summary string `json:"summary"`
			Path    string `json:"path"`
			ReadAt  string `json:"read_at"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	return env.Data
}

func TestNotificationInbox(t *testing.T) {
	inst := startInstance(t)

	aliceKey := inst.newKey(t, "alice")
	bobKey := inst.newKey(t, "bob")
	eveKey := inst.newKey(t, "eve")
	inst.admin(t, "admin", "user", "create", "alice", "--key", aliceKey+".pub")
	inst.admin(t, "admin", "user", "create", "bob", "--key", bobKey+".pub")
	inst.admin(t, "admin", "user", "create", "eve", "--key", eveKey+".pub")

	if _, errOut, code := inst.ssh(t, aliceKey, "", "repo", "create", "alice/app"); code != 0 {
		t.Fatalf("repo create: %s", errOut)
	}

	// Bob opens an issue. The owner hears about it; the actor does not.
	if _, errOut, code := inst.ssh(t, bobKey, "", "issue", "create", "alice/app",
		"--title", "'it leaks'", "--body", "'memory climbs'"); code != 0 {
		t.Fatalf("issue create: %s", errOut)
	}
	got := notices(t, inst, aliceKey)
	if len(got) != 1 || got[0].Actor != "bob" || got[0].Summary != "opened issue #1" ||
		got[0].Repo != "alice/app" || got[0].Kind != "issue" || got[0].Path != "alice/app/issues/1" {
		t.Fatalf("alice inbox = %+v", got)
	}
	if n := notices(t, inst, bobKey); len(n) != 0 {
		t.Fatalf("actor notified about own action: %+v", n)
	}

	// Eve neither owns the repository nor took part in the issue, so
	// nothing has reached her.
	if n := notices(t, inst, eveKey); len(n) != 0 {
		t.Fatalf("uninvolved user notified: %+v", n)
	}

	// Watching widens the recipients to her.
	if _, errOut, code := inst.ssh(t, eveKey, "", "repo", "watch", "alice/app"); code != 0 {
		t.Fatalf("repo watch: %s", errOut)
	}
	if _, _, code := inst.ssh(t, aliceKey, "", "issue", "comment", "alice/app", "1", "--message", "'on it'"); code != 0 {
		t.Fatal("alice comment failed")
	}
	eve := notices(t, inst, eveKey)
	if len(eve) != 1 || eve[0].Summary != "commented on #1" || eve[0].Actor != "alice" {
		t.Fatalf("watcher inbox = %+v", eve)
	}
	if n := len(notices(t, inst, bobKey)); n != 1 {
		t.Fatalf("issue author inbox = %d rows, want 1", n)
	}

	// Muting beats being a participant: bob wrote the issue and still
	// hears nothing more, while the watcher does.
	if _, errOut, code := inst.ssh(t, bobKey, "", "repo", "unwatch", "alice/app"); code != 0 {
		t.Fatalf("repo unwatch: %s", errOut)
	}
	if _, _, code := inst.ssh(t, aliceKey, "", "issue", "comment", "alice/app", "1", "--message", "'more'"); code != 0 {
		t.Fatal("alice comment failed")
	}
	if n := len(notices(t, inst, bobKey)); n != 1 {
		t.Fatalf("muted participant inbox = %d rows, want 1", n)
	}
	if n := len(notices(t, inst, eveKey)); n != 2 {
		t.Fatalf("watcher inbox = %d rows, want 2", n)
	}

	// Reading one drops it from the default list but not from --all.
	eve = notices(t, inst, eveKey)
	if _, errOut, code := inst.ssh(t, eveKey, "", "notifications", "read", strconv.FormatInt(eve[0].ID, 10)); code != 0 {
		t.Fatalf("notifications read: %s", errOut)
	}
	if n := len(notices(t, inst, eveKey)); n != 1 {
		t.Fatalf("unread list = %d rows, want 1", n)
	}
	all := notices(t, inst, eveKey, "--all")
	if len(all) != 2 || all[0].ReadAt == "" {
		t.Fatalf("--all = %+v", all)
	}

	// The dashboard carries the badge count, and --all sweeps it to zero.
	out, _, code := inst.ssh(t, eveKey, "", "dashboard", "--json")
	if code != 0 || !strings.Contains(out, `"unread":1`) {
		t.Fatalf("dashboard unread: %s", out)
	}
	if _, errOut, code := inst.ssh(t, eveKey, "", "notifications", "read", "--all"); code != 0 {
		t.Fatalf("read --all: %s", errOut)
	}
	if n := len(notices(t, inst, eveKey)); n != 0 {
		t.Fatalf("unread after sweep = %d", n)
	}

	// One account cannot mark another's notifications read.
	alice := notices(t, inst, aliceKey)
	if len(alice) == 0 {
		t.Fatal("alice has no notices to test with")
	}
	if out, _, code := inst.ssh(t, eveKey, "", "notifications", "read", strconv.FormatInt(alice[0].ID, 10), "--json"); code != 0 ||
		!strings.Contains(out, `"read":0`) {
		t.Fatalf("cross-account read: %s", out)
	}
	if n := len(notices(t, inst, aliceKey)); n != len(alice) {
		t.Fatal("another account cleared alice's inbox")
	}
}
