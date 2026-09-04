package store

import "testing"

func mrFixture(t *testing.T) (*Store, int64, int64) {
	t.Helper()
	s := open(t)
	if err := s.MigrateUp(); err != nil {
		t.Fatal(err)
	}
	uid, err := s.CreateUser("cmc", true)
	if err != nil {
		t.Fatal(err)
	}
	repoID, err := s.CreateRepo("user", uid, "lib", "public")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateMR(repoID, uid, repoID, "feature", "main", "t", "", "abc123", "md", false); err != nil {
		t.Fatal(err)
	}
	return s, repoID, uid
}

// A merged or closed MR records who resolved it and when: the state alone
// cannot say it, and updated_at moves for every edit.
func TestResolutionStamps(t *testing.T) {
	s, repoID, uid := mrFixture(t)
	mr, err := s.MRByNumber(repoID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if mr.MergedAt != "" || mr.ClosedAt != "" {
		t.Fatalf("open MR carries a stamp: %+v", mr)
	}
	if err := s.MarkMerged(mr.ID, "base1", uid, ""); err != nil {
		t.Fatal(err)
	}
	mr, _ = s.MRByNumber(repoID, 1)
	if mr.State != "merged" || mr.MergedAt == "" || mr.MergedBy != "cmc" {
		t.Fatalf("merge stamp: %+v", mr)
	}

	// Reopening — a source branch that came back — clears the stamp.
	if err := s.SetMRState(mr.ID, "open"); err != nil {
		t.Fatal(err)
	}
	mr, _ = s.MRByNumber(repoID, 1)
	if mr.MergedAt != "" || mr.MergedBy != "" {
		t.Fatalf("reopen kept the merge stamp: %+v", mr)
	}

	if err := s.MarkClosed(mr.ID, uid, ""); err != nil {
		t.Fatal(err)
	}
	mr, _ = s.MRByNumber(repoID, 1)
	if mr.State != "closed" || mr.ClosedAt == "" || mr.ClosedBy != "cmc" {
		t.Fatalf("close stamp: %+v", mr)
	}
}

// An import carries the upstream time but no local account for the actor.
func TestResolutionStampImported(t *testing.T) {
	s, repoID, _ := mrFixture(t)
	mr, _ := s.MRByNumber(repoID, 1)
	if err := s.MarkMerged(mr.ID, "base1", 0, "2024-03-02T10:00:00Z"); err != nil {
		t.Fatal(err)
	}
	mr, _ = s.MRByNumber(repoID, 1)
	if mr.MergedAt != "2024-03-02T10:00:00Z" || mr.MergedBy != "" {
		t.Fatalf("imported merge stamp: %+v", mr)
	}
}
