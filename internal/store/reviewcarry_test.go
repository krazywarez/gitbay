package store

import "testing"

// UpdateMRHead with sameDiff moves the fresh reviews of the old head to
// the new one and leaves already-stale reviews stale (#198).
func TestUpdateMRHeadSameDiffKeepsFreshReviews(t *testing.T) {
	s := open(t)
	if err := s.MigrateUp(); err != nil {
		t.Fatal(err)
	}
	alice, _ := s.CreateUser("alice", false)
	bob, _ := s.CreateUser("bob", false)
	carol, _ := s.CreateUser("carol", false)
	repo, _ := s.CreateRepo("user", alice, "app", "public")
	id, err := s.CreateMR(repo, alice, repo, "feat", "main", "t", "", "aaa", "md", false)
	if err != nil {
		t.Fatal(err)
	}
	// bob reviewed an earlier head and is stale; carol reviewed the
	// current one.
	if err := s.AddMRReview(id, bob, "approve", "000"); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateMRHead(id, "aaa", "", false); err != nil {
		t.Fatal(err)
	}
	if err := s.AddMRReview(id, carol, "approve", "aaa"); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateMRHead(id, "bbb", "", true); err != nil {
		t.Fatal(err)
	}
	reviews, err := s.ListMRReviews(id)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]MRReview{}
	for _, r := range reviews {
		got[r.Reviewer] = r
	}
	if r := got["carol"]; r.Stale || r.HeadSHA != "bbb" {
		t.Errorf("fresh review did not follow the same diff: %+v", r)
	}
	if r := got["bob"]; !r.Stale || r.HeadSHA != "000" {
		t.Errorf("stale review changed: %+v", r)
	}
	// A different diff stales everything, as before.
	if err := s.UpdateMRHead(id, "ccc", "", false); err != nil {
		t.Fatal(err)
	}
	reviews, _ = s.ListMRReviews(id)
	for _, r := range reviews {
		if !r.Stale {
			t.Errorf("review survived a changed diff: %+v", r)
		}
	}
}
