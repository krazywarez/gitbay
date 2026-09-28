package control

import (
	"bytes"
	"errors"
	"testing"

	"gitbay.org/gitbay/internal/protocol"
	"gitbay.org/gitbay/internal/store"
)

// TestIssueCreateWithLabelRequiresWrite checks that attaching a label at
// create time needs the same write access issue label requires, not just
// the read access that lets anyone file the issue in the first place.
func TestIssueCreateWithLabelRequiresWrite(t *testing.T) {
	c := notifTestCtx(t, "alice")
	repoID, err := c.Store.CreateRepo("user", c.User.ID, "app", "public")
	if err != nil {
		t.Fatal(err)
	}
	repo, err := c.Store.RepoByID(repoID)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Store.SetLabel(repo, "bug", "ff0000"); err != nil {
		t.Fatal(err)
	}
	bobID, err := c.Store.CreateUser("bob", false)
	if err != nil {
		t.Fatal(err)
	}
	bob := *c
	bob.User = store.User{ID: bobID, Username: "bob"}
	var out bytes.Buffer
	bob.Stdout, bob.Stderr = &out, &out

	if code := runIssueCreate(&bob, []string{repo.Path(), "--title", "t", "--label", "bug"}); code != protocol.ExitDenied {
		t.Fatalf("exit %d, want %d", code, protocol.ExitDenied)
	}
	if _, err := c.Store.IssueByNumber(repo.ID, 1); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("issue was created despite the denial: %v", err)
	}
}

func TestIssueCreateSetsLabelsMilestoneAndAssignee(t *testing.T) {
	c := notifTestCtx(t, "alice")
	repoID, err := c.Store.CreateRepo("user", c.User.ID, "app", "public")
	if err != nil {
		t.Fatal(err)
	}
	repo, err := c.Store.RepoByID(repoID)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Store.SetLabel(repo, "bug", "ff0000"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Store.CreateMilestone(repo, "m1", "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Store.CreateUser("bob", false); err != nil {
		t.Fatal(err)
	}

	if code := runIssueCreate(c, []string{repo.Path(), "--title", "t",
		"--label", "bug", "--milestone", "m1", "--assignee", "bob"}); code != 0 {
		t.Fatalf("exit %d", code)
	}
	issue, err := c.Store.IssueByNumber(repo.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(issue.Labels) != 1 || issue.Labels[0] != "bug" {
		t.Errorf("labels = %v", issue.Labels)
	}
	if issue.Milestone != "m1" {
		t.Errorf("milestone = %q", issue.Milestone)
	}
	if len(issue.Assignees) != 1 || issue.Assignees[0] != "bob" {
		t.Errorf("assignees = %v", issue.Assignees)
	}
}

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
