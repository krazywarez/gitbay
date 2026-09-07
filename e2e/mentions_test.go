package e2e

import (
	"strings"
	"testing"
)

// An @mention files an inbox row for the mentioned account and makes them
// a participant of the thread; watchers are not told about the mention,
// mute holds, and someone who cannot read the repository is not reached
// (#202).
func TestMentionsNotify(t *testing.T) {
	inst := startInstance(t)
	keys := map[string]string{}
	for _, u := range []string{"alice", "bob", "carol", "eve"} {
		keys[u] = inst.newKey(t, u)
		inst.admin(t, "admin", "user", "create", u, "--key", keys[u]+".pub")
	}
	if _, errOut, code := inst.ssh(t, keys["alice"], "", "repo", "create", "alice/app"); code != 0 {
		t.Fatalf("repo create: %s", errOut)
	}
	if _, _, code := inst.ssh(t, keys["carol"], "", "repo", "watch", "alice/app"); code != 0 {
		t.Fatal("watch failed")
	}
	summaries := func(key string) []string {
		var out []string
		for _, n := range notices(t, inst, key) {
			out = append(out, n.Summary)
		}
		return out
	}
	has := func(list []string, want string) bool {
		for _, s := range list {
			if strings.Contains(s, want) {
				return true
			}
		}
		return false
	}

	// A mention in an issue body, with prose punctuation after the name.
	if _, errOut, code := inst.ssh(t, keys["alice"], "", "issue", "create", "alice/app",
		"--title", "'leak'", "--body", "'@bob, can you look? cc @nobody and @alice.'"); code != 0 {
		t.Fatalf("issue create: %s", errOut)
	}
	if got := summaries(keys["bob"]); !has(got, "mentioned you in #1") {
		t.Fatalf("bob not told about the mention: %v", got)
	}
	if got := summaries(keys["carol"]); has(got, "mentioned") || !has(got, "opened issue #1") {
		t.Fatalf("watcher inbox: %v", got)
	}
	if got := summaries(keys["alice"]); has(got, "mentioned") {
		t.Fatalf("self-mention filed: %v", got)
	}

	// bob is now a participant: a later comment reaches him.
	if _, errOut, code := inst.ssh(t, keys["alice"], "", "issue", "comment", "alice/app", "1", "--message", "'more'"); code != 0 {
		t.Fatalf("comment: %s", errOut)
	}
	if got := summaries(keys["bob"]); !has(got, "commented on #1") {
		t.Fatalf("mentioned account not a participant: %v", got)
	}

	// Muted, a mention does not get through.
	if _, _, code := inst.ssh(t, keys["bob"], "", "repo", "mute", "alice/app"); code != 0 {
		t.Fatal("mute failed")
	}
	if _, errOut, code := inst.ssh(t, keys["alice"], "", "issue", "comment", "alice/app", "1", "--message", "'@bob again'"); code != 0 {
		t.Fatalf("comment: %s", errOut)
	}
	if got := summaries(keys["bob"]); has(got, "mentioned you in #1") && len(got) > 2 {
		t.Fatalf("mention reached a muted account: %v", got)
	}

	// A private repository: a mention of someone who cannot read it is
	// dropped, one of someone who can is filed. Merge request bodies too.
	for _, args := range [][]string{
		{"repo", "create", "alice/secret", "--private"},
		{"repo", "access", "grant", "alice/secret", "carol", "read"},
		{"issue", "create", "alice/secret", "--title", "'hush'", "--body", "'@eve @carol'"},
	} {
		if _, errOut, code := inst.ssh(t, keys["alice"], "", args...); code != 0 {
			t.Fatalf("%v: %s", args, errOut)
		}
	}
	if got := summaries(keys["eve"]); len(got) != 0 {
		t.Fatalf("outsider reached through a private repository: %v", got)
	}
	if got := summaries(keys["carol"]); !has(got, "mentioned you in #1") {
		t.Fatalf("reader of a private repository not told: %v", got)
	}
}
