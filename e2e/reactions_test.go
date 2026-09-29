package e2e

import (
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Reactions on issues, merge requests and their comments (#291): the
// commands, the counts in show, the web buttons, and a private
// repository staying invisible.
func TestReactions(t *testing.T) {
	t.Parallel()
	inst := startInstanceWith(t, "[web]\nmode = \"accounts\"\n")
	aliceKey := inst.newKey(t, "alice")
	bobKey := inst.newKey(t, "bob")
	carolKey := inst.newKey(t, "carol")
	inst.admin(t, "admin", "user", "create", "alice", "--key", aliceKey+".pub")
	inst.admin(t, "admin", "user", "create", "bob", "--key", bobKey+".pub")
	inst.admin(t, "admin", "user", "create", "carol", "--key", carolKey+".pub")
	for _, args := range [][]string{{"repo", "create", "alice/app"}, {"repo", "create", "alice/secret", "--private"}} {
		if _, errOut, code := inst.ssh(t, aliceKey, "", args...); code != 0 {
			t.Fatalf("%v: %s", args, errOut)
		}
	}
	if _, _, code := inst.ssh(t, aliceKey, "", "repo", "access", "grant", "alice/secret", "bob", "read"); code != 0 {
		t.Fatal("grant failed")
	}
	env := inst.gitEnv(aliceKey)
	work := t.TempDir()
	mustGit(t, work, env, "clone", inst.sshURL("alice/app"), "w")
	dir := filepath.Join(work, "w")
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a\n"), 0o644)
	mustGit(t, dir, env, "checkout", "-q", "-b", "main")
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, env, "commit", "-q", "-m", "base")
	mustGit(t, dir, env, "push", "-q", "origin", "main")
	mustGit(t, dir, env, "checkout", "-q", "-b", "feat")
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("b\n"), 0o644)
	mustGit(t, dir, env, "commit", "-q", "-am", "change")
	mustGit(t, dir, env, "push", "-q", "origin", "feat")

	must := func(key string, args ...string) string {
		t.Helper()
		out, errOut, code := inst.ssh(t, key, "", args...)
		if code != 0 {
			t.Fatalf("%v: exit %d: %s%s", args, code, out, errOut)
		}
		return out
	}
	must(aliceKey, "issue", "create", "alice/app", "--title", "bug", "--body", "'it breaks'")
	must(aliceKey, "issue", "comment", "alice/app", "1", "--message", "first")
	must(aliceKey, "mr", "create", "alice/app", "--source", "feat", "--target", "main", "--title", "'change'")
	must(aliceKey, "mr", "comment", "alice/app", "1", "--message", "'looks fine'")
	must(aliceKey, "issue", "create", "alice/secret", "--title", "hidden")

	type show struct {
		Data struct {
			Reactions []struct {
				Reaction string
				Count    int
				Me       bool
			}
			Comments []struct {
				ID        int64
				Reactions []struct {
					Reaction string
					Count    int
					Me       bool
				}
			}
		}
	}
	read := func(key, noun string) show {
		t.Helper()
		var s show
		if err := json.Unmarshal([]byte(must(key, noun, "show", "alice/app", "1", "--json")), &s); err != nil {
			t.Fatal(err)
		}
		return s
	}

	for _, noun := range []string{"issue", "mr"} {
		cid := read(bobKey, noun).Data.Comments[0].ID
		if cid == 0 {
			t.Fatalf("%s show carries no comment id", noun)
		}
		must(bobKey, noun, "react", "alice/app", "1", "+1")
		must(bobKey, noun, "react", "alice/app", "1", "+1") // idempotent
		must(aliceKey, noun, "react", "alice/app", "1", "👍")
		must(bobKey, noun, "react", "alice/app", "1", "--comment", itoa64(cid), "-1")
		must(bobKey, noun, "react", "alice/app", "1", "--remove", "rocket") // absent: no-op

		s := read(bobKey, noun)
		if len(s.Data.Reactions) != 1 || s.Data.Reactions[0].Reaction != "+1" || s.Data.Reactions[0].Count != 2 || !s.Data.Reactions[0].Me {
			t.Errorf("%s body reactions: %+v", noun, s.Data.Reactions)
		}
		if c := s.Data.Comments[0].Reactions; len(c) != 1 || c[0].Reaction != "-1" || c[0].Count != 1 || !c[0].Me {
			t.Errorf("%s comment reactions: %+v", noun, c)
		}
		if a := read(aliceKey, noun); a.Data.Comments[0].Reactions[0].Me {
			t.Errorf("%s: alice marked on bob's reaction", noun)
		}
		if out := must(bobKey, noun, "show", "alice/app", "1"); !strings.Contains(out, "👍 2 (you)") || !strings.Contains(out, "(comment ") {
			t.Errorf("%s show text:\n%s", noun, out)
		}
		if _, _, code := inst.ssh(t, bobKey, "", noun, "react", "alice/app", "1", "nope"); code != 2 {
			t.Errorf("%s: unknown reaction exited %d, want 2", noun, code)
		}
		if _, _, code := inst.ssh(t, bobKey, "", noun, "react", "alice/app", "1", "--comment", "9999", "+1"); code != 3 {
			t.Errorf("%s: unknown comment exited %d, want 3", noun, code)
		}
	}

	// Private: readable by bob (granted), not found for carol.
	must(bobKey, "issue", "react", "alice/secret", "1", "eyes")
	if _, _, code := inst.ssh(t, carolKey, "", "issue", "react", "alice/secret", "1", "eyes"); code != 3 {
		t.Errorf("private repository: exit %d, want 3", code)
	}

	// Reacting files no activity.
	if out := must(aliceKey, "feed", "--json"); strings.Contains(out, "react") {
		t.Errorf("a reaction reached the feed:\n%s", out)
	}

	// Web: buttons for a signed-in viewer, counts only for anyone else.
	bob := inst.login(t, bobKey)
	if status, page := browserGet(t, bob, inst.base()+"/alice/app/issues/1"); status != 200 ||
		!strings.Contains(page, `class="react mine"`) || !strings.Contains(page, `name="add" value="eyes"`) {
		t.Errorf("signed-in issue page: %d\n%s", status, page)
	}
	if status, page := browserGet(t, bob, inst.base()+"/alice/app/mrs/1"); status != 200 || !strings.Contains(page, `class="react mine"`) {
		t.Errorf("signed-in MR page: %d", status)
	}
	if status, _ := browserPost(t, bob, inst.base()+"/alice/app/issues/1/react", url.Values{"add": {"rocket"}}); status != 200 {
		t.Fatalf("web react: %d", status)
	}
	if status, _ := browserPost(t, bob, inst.base()+"/alice/app/mrs/1/react", url.Values{"remove": {"+1"}}); status != 200 {
		t.Fatalf("web unreact: %d", status)
	}
	if s := read(bobKey, "issue"); len(s.Data.Reactions) != 2 {
		t.Errorf("web reaction not stored: %+v", s.Data.Reactions)
	}
	if s := read(bobKey, "mr"); s.Data.Reactions[0].Count != 1 || s.Data.Reactions[0].Me {
		t.Errorf("web removal: %+v", s.Data.Reactions)
	}
	_, anon := inst.get(t, "/alice/app/issues/1")
	if strings.Contains(anon, "/issues/1/react") || !strings.Contains(anon, "👍 2") {
		t.Errorf("signed-out issue page:\n%s", anon)
	}
}

func itoa64(n int64) string { b, _ := json.Marshal(n); return string(b) }
