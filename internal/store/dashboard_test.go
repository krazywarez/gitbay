package store

import (
	"reflect"
	"testing"
)

// Rows sharing an updated_at (timestamps are milliseconds) come back
// newest first, the same order distinct times give, so the dashboard does
// not reshuffle depending on whether two writes landed in one millisecond.
func TestDashboardEqualUpdatedAtStableOrder(t *testing.T) {
	s := open(t)
	if err := s.MigrateUp(); err != nil {
		t.Fatal(err)
	}
	uid, err := s.CreateUser("cmc", false)
	if err != nil {
		t.Fatal(err)
	}
	other, err := s.CreateUser("alice", false)
	if err != nil {
		t.Fatal(err)
	}
	repoID, err := s.CreateRepo("user", uid, "gitbay", "public")
	if err != nil {
		t.Fatal(err)
	}
	for _, title := range []string{"first", "second", "third"} {
		n, err := s.CreateIssue(repoID, uid, title, "", "markdown")
		if err != nil {
			t.Fatal(err)
		}
		issue, err := s.IssueByNumber(repoID, n)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.SetIssueAssignee(issue.ID, other, true); err != nil {
			t.Fatal(err)
		}
		if _, err := s.CreateMR(repoID, other, repoID, title, "main", title, "", "abc", "md", false); err != nil {
			t.Fatal(err)
		}
	}
	for _, table := range []string{"issues", "merge_requests"} {
		if _, err := s.DB.Exec("UPDATE " + table + " SET updated_at = '2026-10-01T12:00:00.000Z'"); err != nil {
			t.Fatal(err)
		}
	}

	titles := func(items []DashboardItem, err error) []string {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, d := range items {
			out = append(out, d.Title)
		}
		return out
	}
	want := []string{"third", "second", "first"}
	cases := []struct {
		name string
		got  []string
	}{
		{"DashboardIssues", titles(s.DashboardIssues(uid))},
		{"DashboardMRs", titles(s.DashboardMRs(uid))},
		{"ReviewQueue", titles(s.ReviewQueue(uid))},
		{"AssignedIssues", titles(s.AssignedIssues(other))},
	}
	for _, tc := range cases {
		if !reflect.DeepEqual(tc.got, want) {
			t.Errorf("%s = %v, want %v", tc.name, tc.got, want)
		}
	}
}

// An issue assigned to the user is not repeated under DashboardIssues:
// AssignedIssues already covers it, and a repository the user can
// otherwise reach (here, one they own) is the common case where the two
// queries used to overlap (#265).
func TestDashboardIssuesExcludesAssignedIssues(t *testing.T) {
	s := open(t)
	if err := s.MigrateUp(); err != nil {
		t.Fatal(err)
	}
	uid, err := s.CreateUser("cmc", false)
	if err != nil {
		t.Fatal(err)
	}
	repoID, err := s.CreateRepo("user", uid, "gitbay", "public")
	if err != nil {
		t.Fatal(err)
	}
	repo, err := s.RepoByID(repoID)
	if err != nil {
		t.Fatal(err)
	}
	assignedNum, err := s.CreateIssue(repo.ID, uid, "assigned to me", "", "markdown")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateIssue(repo.ID, uid, "not assigned", "", "markdown"); err != nil {
		t.Fatal(err)
	}
	assigned, err := s.IssueByNumber(repo.ID, assignedNum)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetIssueAssignee(assigned.ID, uid, true); err != nil {
		t.Fatal(err)
	}

	issues, err := s.DashboardIssues(uid)
	if err != nil {
		t.Fatal(err)
	}
	if len(issues) != 1 || issues[0].Title != "not assigned" {
		t.Fatalf("DashboardIssues = %+v, want only the unassigned issue", issues)
	}
	assignedList, err := s.AssignedIssues(uid)
	if err != nil {
		t.Fatal(err)
	}
	if len(assignedList) != 1 || assignedList[0].Title != "assigned to me" {
		t.Fatalf("AssignedIssues = %+v, want the assigned issue", assignedList)
	}
}
