package store

import (
	"errors"
	"testing"
)

func TestOrgMilestoneSpansRepos(t *testing.T) {
	f := newAcme(t)
	id, folded, err := f.s.CreateOrgMilestone(f.org, "v1", "first", "2027-01-01")
	if err != nil || folded != 0 || id == 0 {
		t.Fatalf("CreateOrgMilestone: %d, %d, %v", id, folded, err)
	}
	// Resolves from either repo, not from alice/app.
	m, err := f.s.MilestoneByTitle(f.core, "v1")
	if err != nil || m.OrgID != f.org || m.RepoID != 0 {
		t.Fatalf("core resolves v1 = %+v, %v", m, err)
	}
	if _, err := f.s.MilestoneByTitle(f.app, "v1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("app resolves v1: %v", err)
	}
	if err := f.s.SetIssueMilestone(f.coreIssue, id); err != nil {
		t.Fatal(err)
	}
	if err := f.s.SetIssueMilestone(f.siteIssue, id); err != nil {
		t.Fatal(err)
	}
	if err := f.s.SetIssueState(f.siteIssue, "closed"); err != nil {
		t.Fatal(err)
	}
	ms, err := f.s.ListOrgMilestones(f.org, "open", f.orgRepos())
	if err != nil || len(ms) != 1 || ms[0].OpenItems != 1 || ms[0].ClosedItems != 1 {
		t.Fatalf("org list = %+v, %v", ms, err)
	}
	// Counts stop at what the caller can read.
	ms, _ = f.s.ListOrgMilestones(f.org, "open", []int64{f.core.ID})
	if ms[0].OpenItems != 1 || ms[0].ClosedItems != 0 {
		t.Fatalf("org list over core = %+v", ms)
	}
	// A repo's list shows the org milestone first, then its own.
	if _, err := f.s.CreateMilestone(f.core, "core-only", "", ""); err != nil {
		t.Fatal(err)
	}
	ms, _ = f.s.ListMilestones(f.core, "open", f.orgRepos())
	if len(ms) != 2 || ms[0].Title != "v1" || ms[0].OrgID != f.org || ms[1].Title != "core-only" || ms[1].RepoID != f.core.ID {
		t.Fatalf("core list = %+v", ms)
	}
	if _, err := f.s.OrgMilestoneByTitle(f.org, "core-only"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("org resolves a repo milestone: %v", err)
	}
}

func TestRepoMilestoneRefusedWhenOrgHoldsTitle(t *testing.T) {
	f := newAcme(t)
	if _, _, err := f.s.CreateOrgMilestone(f.org, "v1", "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.CreateMilestone(f.core, "v1", "", ""); !errors.Is(err, ErrOrgScoped) {
		t.Fatalf("CreateMilestone over org title: %v", err)
	}
	if _, err := f.s.CreateMilestone(f.app, "v1", "", ""); err != nil {
		t.Fatalf("user repo unaffected: %v", err)
	}
	if _, _, err := f.s.CreateOrgMilestone(f.org, "v1", "", ""); err == nil {
		t.Fatal("duplicate org milestone accepted")
	}
}

func TestCreateOrgMilestonePromotes(t *testing.T) {
	f := newAcme(t)
	cid, err := f.s.CreateMilestone(f.core, "v1", "", "")
	if err != nil {
		t.Fatal(err)
	}
	sid, err := f.s.CreateMilestone(f.site, "v1", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.s.SetIssueMilestone(f.coreIssue, cid); err != nil {
		t.Fatal(err)
	}
	n, err := f.s.CreateMR(f.site.ID, f.alice, f.site.ID, "feat", "main", "t", "", "abc", "md", false)
	if err != nil {
		t.Fatal(err)
	}
	// CreateMR returns the per-repo MR number, not the merge_requests.id
	// row that milestone_id references; resolve it the way SetMRMilestone
	// callers must.
	mr, err := f.s.MRByNumber(f.site.ID, n)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.s.SetMRMilestone(mr.ID, sid); err != nil {
		t.Fatal(err)
	}
	id, folded, err := f.s.CreateOrgMilestone(f.org, "v1", "org wide", "2027-06-01")
	if err != nil || folded != 2 {
		t.Fatalf("promote: folded %d, %v", folded, err)
	}
	var count int
	f.s.DB.QueryRow("SELECT COUNT(*) FROM milestones WHERE title = 'v1'").Scan(&count)
	if count != 1 {
		t.Fatalf("milestones named v1: %d", count)
	}
	f.s.DB.QueryRow("SELECT COUNT(*) FROM issues WHERE milestone_id = ?", id).Scan(&count)
	if count != 1 {
		t.Fatalf("issues on org milestone: %d", count)
	}
	f.s.DB.QueryRow("SELECT COUNT(*) FROM merge_requests WHERE milestone_id = ?", id).Scan(&count)
	if count != 1 {
		t.Fatalf("mrs on org milestone: %d", count)
	}
	ms, _ := f.s.ListOrgMilestones(f.org, "open", f.orgRepos())
	if len(ms) != 1 || ms[0].OpenItems != 2 || ms[0].Description != "org wide" || ms[0].DueDate != "2027-06-01" {
		t.Fatalf("after promote: %+v", ms)
	}
}
