package e2e

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

func TestActivityGraph(t *testing.T) {
	inst := startInstance(t)
	aliceKey := inst.newKey(t, "alice")
	bobKey := inst.newKey(t, "bob")
	inst.admin(t, "admin", "user", "create", "alice",
		"--key", aliceKey+".pub", "--email", "alice@example.test", "--verified")
	// bob's email is NOT verified: his commits must not attribute.
	inst.admin(t, "admin", "user", "create", "bob", "--key", bobKey+".pub")

	if _, _, code := inst.ssh(t, aliceKey, "", "repo", "create", "alice/app"); code != 0 {
		t.Fatal("repo create failed")
	}
	if _, _, code := inst.ssh(t, aliceKey, "", "repo", "access", "grant", "alice/app", "bob", "write"); code != 0 {
		t.Fatal("grant failed")
	}

	work := t.TempDir()
	env := inst.gitEnv(aliceKey)
	// gitEnv pins GIT_AUTHOR_EMAIL; author identity comes from the env.
	// Clone before appending: sharing env's backing array would let the
	// second append clobber the first.
	aliceEnv := append(slices.Clone(env), "GIT_AUTHOR_EMAIL=alice@example.test", "GIT_AUTHOR_NAME=Alice")
	bobEnv := append(slices.Clone(env), "GIT_AUTHOR_EMAIL=bob@nowhere.test", "GIT_AUTHOR_NAME=Bob")
	mustGit(t, work, env, "clone", inst.sshURL("alice/app"), "w")
	dir := filepath.Join(work, "w")
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a\n"), 0o644)
	mustGit(t, dir, env, "checkout", "-q", "-b", "main")
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, aliceEnv, "commit", "-q", "-m", "one")
	os.WriteFile(filepath.Join(dir, "b.txt"), []byte("b\n"), 0o644)
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, bobEnv, "commit", "-q", "-m", "two")
	mustGit(t, dir, env, "push", "-q", "origin", "main")

	// Today's cell counts alice's commit; bob's unverified authorship
	// contributes nothing and issue creation counts as an event.
	if _, _, code := inst.ssh(t, aliceKey, "", "issue", "create", "alice/app", "--title", "'x'"); code != 0 {
		t.Fatal("issue create failed")
	}
	status, body := inst.get(t, "/alice")
	if status != 200 || !strings.Contains(body, `class="actgraph"`) {
		t.Fatalf("graph missing: %d", status)
	}
	// alice: 1 attributed commit + events (issue.created, ...). The exact
	// cells depend on author-date timezone vs event UTC, so assert the
	// year total instead.
	total := activityTotal(t, body)
	if total < 2 {
		t.Fatalf("alice total = %d, want >= 2", total)
	}
	// bob authored a commit but his email is unverified: zero activity.
	_, bobBody := inst.get(t, "/bob")
	if bt := activityTotal(t, bobBody); bt != 0 {
		t.Fatalf("unverified author got credit: total %d", bt)
	}

	// Re-pushing the same history (force) does not double-count.
	mustGit(t, dir, env, "push", "-q", "--force", "origin", "main")
	_, body2 := inst.get(t, "/alice")
	if body2 != body {
		// Counts must be identical; compare just the graph cells.
		if excerpt(body, "actgraph") != excerpt(body2, "actgraph") {
			t.Fatal("re-push changed activity counts")
		}
	}

	// Org pages aggregate their repos' activity.
	if _, _, code := inst.ssh(t, aliceKey, "", "org", "create", "theorg"); code != 0 {
		t.Fatal("org create failed")
	}
	if _, _, code := inst.ssh(t, aliceKey, "", "repo", "transfer", "alice/app", "theorg"); code != 0 {
		t.Fatal("transfer failed")
	}
	_, orgBody := inst.get(t, "/theorg")
	if !strings.Contains(orgBody, `class="actgraph"`) || strings.Contains(orgBody, "0 in the last year") {
		t.Fatalf("org graph empty:\n%s", excerpt(orgBody, "activity"))
	}

	// Backfill is idempotent and attributes only verified authors.
	out := inst.admin(t, "admin", "backfill-activity")
	if !strings.Contains(out, "theorg/app\t0 attributed") {
		t.Fatalf("backfill re-attributed existing commits: %s", out)
	}
}

// excerpt returns ~600 bytes around the first occurrence of marker.
func excerpt(s, marker string) string {
	i := strings.Index(s, marker)
	if i < 0 {
		return "(marker missing)"
	}
	end := i + 600
	if end > len(s) {
		end = len(s)
	}
	return s[i:end]
}

var totalPat = regexp.MustCompile(`([0-9]+) in the last year`)

func activityTotal(t *testing.T, body string) int {
	t.Helper()
	m := totalPat.FindStringSubmatch(body)
	if m == nil {
		t.Fatal("activity total missing from page")
	}
	n, _ := strconv.Atoi(m[1])
	return n
}
