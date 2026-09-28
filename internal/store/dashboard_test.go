package store

import "testing"

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
