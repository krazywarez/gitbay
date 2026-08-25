package e2e

import (
	"path/filepath"
	"strings"
	"testing"

	"gitbay.org/gitbay/internal/store"
)

func TestMigrateCommitRefComments(t *testing.T) {
	inst := startInstance(t)
	aliceKey := inst.newKey(t, "alice")
	inst.admin(t, "admin", "user", "create", "alice", "--key", aliceKey+".pub")
	if _, _, code := inst.ssh(t, aliceKey, "", "repo", "create", "alice/app"); code != 0 {
		t.Fatal("repo create failed")
	}
	// Two issues; the migration should touch commit-ref comments only.
	inst.ssh(t, aliceKey, "", "issue", "create", "alice/app", "--title", "'bug'")
	inst.ssh(t, aliceKey, "", "issue", "create", "alice/app", "--title", "'other'")
	// A plain human comment that must be left alone.
	inst.ssh(t, aliceKey, "", "issue", "comment", "alice/app", "2", "--message", "'just a note'")

	// Simulate a legacy commit-reference comment: author-attributed
	// (kind='comment'), bare short sha, the pre-system-message format.
	dbPath := filepath.Join(inst.root, "gitbay.db")
	legacy := "referenced in commit 2e6467a72f: Audit logging, auth throttling, pack limits, user disable"
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	_, err = st.DB.Exec(
		"INSERT INTO issue_comments (issue_id, author_id, body, kind) "+
			"VALUES ((SELECT id FROM issues WHERE number=1), "+
			"(SELECT id FROM users WHERE username='alice'), ?, 'comment')", legacy)
	st.Close()
	if err != nil {
		t.Fatal(err)
	}

	// Before: it renders as a comment card from alice, no link.
	_, body := inst.get(t, "/alice/app/issues/1")
	if !strings.Contains(body, "referenced in commit 2e6467a72f") ||
		strings.Contains(body, "/alice/app/commit/2e6467a72f") {
		t.Fatal("precondition: legacy comment not seeded as expected")
	}

	out := inst.admin(t, "admin", "migrate-commit-refs")
	if !strings.Contains(out, "converted 1 commit-reference comment") {
		t.Fatalf("migrate output: %s", out)
	}

	// After: a system message with a linked sha.
	_, body = inst.get(t, "/alice/app/issues/1")
	if !strings.Contains(body, `class="syscomment"`) ||
		!strings.Contains(body, `href="/alice/app/commit/2e6467a72f"`) {
		t.Fatalf("comment not converted:\n%s", body)
	}
	show, _, _ := inst.ssh(t, aliceKey, "", "issue", "show", "alice/app", "1", "--json")
	if !strings.Contains(show, `"author":"system"`) {
		t.Fatalf("author not system: %s", show)
	}

	// The human comment on issue 2 is untouched.
	out, _, _ = inst.ssh(t, aliceKey, "", "issue", "show", "alice/app", "2", "--json")
	if !strings.Contains(out, "just a note") || strings.Contains(out, `"author":"system"`) {
		t.Fatalf("human comment altered: %s", out)
	}

	// Re-running converts nothing (idempotent).
	if out := inst.admin(t, "admin", "migrate-commit-refs"); !strings.Contains(out, "converted 0") {
		t.Fatalf("re-run not idempotent: %s", out)
	}
}
