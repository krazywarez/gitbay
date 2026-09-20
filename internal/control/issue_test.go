package control

import "testing"

func TestIssueAssignNotifiesTheAssignee(t *testing.T) {
	c, repo, bob := testRepoWithWatcher(t)

	if code := runIssueAssign(c, []string{repo.Path(), "1", "--add", "bob"}); code != 0 {
		t.Fatalf("exit %d", code)
	}
	rows, _ := c.Store.Inbox(bob, false, 20, 0)
	if len(rows) != 1 || rows[0].Summary != "assigned you to #1" {
		t.Fatalf("got %+v", rows)
	}
}

// The spec files a notice for each account newly added. SetIssueAssignee
// inserts ON CONFLICT DO NOTHING and reports nothing either way, so a
// client reconciling an assignee list by re-sending the whole set would
// file, mail and push a row on every save.
func TestIssueAssignDoesNotRenotifyAnExistingAssignee(t *testing.T) {
	c, repo, bob := testRepoWithWatcher(t)
	c.Store.AddPushDevice(bob, "tok-b", "iphone")

	if code := runIssueAssign(c, []string{repo.Path(), "1", "--add", "bob"}); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if code := runIssueAssign(c, []string{repo.Path(), "1", "--add", "bob"}); code != 0 {
		t.Fatalf("exit %d", code)
	}

	rows, _ := c.Store.Inbox(bob, false, 20, 0)
	if len(rows) != 1 {
		t.Fatalf("filed %d rows for one assignment: %+v", len(rows), rows)
	}
	if due, _ := c.Store.DuePush(20); len(due) != 1 {
		t.Fatalf("queued %d pushes for one assignment", len(due))
	}
	// The second call still succeeds and still reports the assignee.
	updated, err := c.Store.IssueByNumber(repo.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(updated.Assignees) != 1 || updated.Assignees[0] != "bob" {
		t.Fatalf("assignees: %+v", updated.Assignees)
	}
}

func TestIssueAssignIsSilentForTheActorAndForRemovals(t *testing.T) {
	c, repo, bob := testRepoWithWatcher(t)

	// Assigning yourself announces nothing: notify drops the actor.
	runIssueAssign(c, []string{repo.Path(), "1", "--add", "alice"})
	if rows, _ := c.Store.Inbox(c.User.ID, false, 20, 0); len(rows) != 0 {
		t.Fatalf("self-assignment notified: %+v", rows)
	}

	// Unassigning files nothing.
	runIssueAssign(c, []string{repo.Path(), "1", "--add", "bob"})
	before, _ := c.Store.Inbox(bob, false, 20, 0)
	runIssueAssign(c, []string{repo.Path(), "1", "--remove", "bob"})
	after, _ := c.Store.Inbox(bob, false, 20, 0)
	if len(after) != len(before) {
		t.Fatalf("removal filed a row: %d then %d", len(before), len(after))
	}
}
