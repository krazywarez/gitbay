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
