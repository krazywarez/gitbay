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

// A merge request closed without merging can record the request that
// carried its change forward; MRsSuperseding is the reverse lookup, and
// 0 clears the field (#223).
func TestSupersededBy(t *testing.T) {
	s, repoID, uid := mrFixture(t)
	if _, err := s.CreateMR(repoID, uid, repoID, "feature2", "main", "t2", "", "def456", "md", false); err != nil {
		t.Fatal(err)
	}
	mr1, _ := s.MRByNumber(repoID, 1)
	if err := s.MarkClosed(mr1.ID, uid, ""); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSupersededBy(mr1.ID, 2); err != nil {
		t.Fatal(err)
	}
	mr1, _ = s.MRByNumber(repoID, 1)
	if mr1.SupersededBy != 2 {
		t.Fatalf("SupersededBy = %d, want 2", mr1.SupersededBy)
	}
	superseding, err := s.MRsSuperseding(repoID, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(superseding) != 1 || superseding[0].Number != 1 {
		t.Fatalf("MRsSuperseding(repoID, 2) = %+v", superseding)
	}
	if err := s.SetSupersededBy(mr1.ID, 0); err != nil {
		t.Fatal(err)
	}
	mr1, _ = s.MRByNumber(repoID, 1)
	if mr1.SupersededBy != 0 {
		t.Fatalf("SupersededBy after clear = %d, want 0", mr1.SupersededBy)
	}
	superseding, err = s.MRsSuperseding(repoID, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(superseding) != 0 {
		t.Fatalf("MRsSuperseding(repoID, 2) after clear = %+v", superseding)
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

// PreferredVerifiedEmail falls back to a verified secondary when the
// primary is not verified, unlike PrimaryVerifiedEmail (#158).
func TestPreferredVerifiedEmail(t *testing.T) {
	s := open(t)
	if err := s.MigrateUp(); err != nil {
		t.Fatal(err)
	}
	uid, err := s.CreateUser("gus", false)
	if err != nil {
		t.Fatal(err)
	}

	if addr, err := s.PreferredVerifiedEmail(uid); err != nil || addr != "" {
		t.Fatalf("no addresses at all: %q, %v", addr, err)
	}

	if err := s.AddEmail(uid, "primary@example.test", "", true); err != nil {
		t.Fatal(err)
	}
	if addr, err := s.PreferredVerifiedEmail(uid); err != nil || addr != "" {
		t.Fatalf("unverified primary only: %q, %v", addr, err)
	}
	if addr, err := s.PrimaryVerifiedEmail(uid); err != nil || addr != "" {
		t.Fatalf("PrimaryVerifiedEmail on an unverified primary: %q, %v", addr, err)
	}

	if err := s.AddEmail(uid, "secondary@example.test", "smtp", false); err != nil {
		t.Fatal(err)
	}
	if addr, err := s.PreferredVerifiedEmail(uid); err != nil || addr != "secondary@example.test" {
		t.Fatalf("unverified primary, verified secondary: %q, %v", addr, err)
	}
	// PrimaryVerifiedEmail keeps meaning exactly what it says: still "",
	// because the primary itself is still unverified.
	if addr, err := s.PrimaryVerifiedEmail(uid); err != nil || addr != "" {
		t.Fatalf("PrimaryVerifiedEmail with only the secondary verified: %q, %v", addr, err)
	}

	if err := s.VerifyEmail(uid, "primary@example.test", "admin"); err != nil {
		t.Fatal(err)
	}
	if addr, err := s.PreferredVerifiedEmail(uid); err != nil || addr != "primary@example.test" {
		t.Fatalf("both verified, primary should win: %q, %v", addr, err)
	}
}

// With no verified primary, the choice among verified secondaries must not
// depend on insertion or row order.
func TestPreferredVerifiedEmailDeterministicTiebreak(t *testing.T) {
	s := open(t)
	if err := s.MigrateUp(); err != nil {
		t.Fatal(err)
	}
	uid, err := s.CreateUser("gus", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AddEmail(uid, "primary@example.test", "", true); err != nil {
		t.Fatal(err)
	}
	if err := s.AddEmail(uid, "zzz@example.test", "smtp", false); err != nil {
		t.Fatal(err)
	}
	if err := s.AddEmail(uid, "aaa@example.test", "smtp", false); err != nil {
		t.Fatal(err)
	}
	if addr, err := s.PreferredVerifiedEmail(uid); err != nil || addr != "aaa@example.test" {
		t.Fatalf("tiebreak should be alphabetical: %q, %v", addr, err)
	}
}

// MRCommentCounts folds conversation comments and diff-thread roots into
// one count per MR, for the list page. System comments, diff-thread
// replies, and pending (unpublished) diff comments do not count.
func TestMRCommentCounts(t *testing.T) {
	s, repoID, uid := mrFixture(t)
	mr1, err := s.MRByNumber(repoID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateMR(repoID, uid, repoID, "feature2", "main", "t2", "", "def456", "md", false); err != nil {
		t.Fatal(err)
	}
	mr2, err := s.MRByNumber(repoID, 2)
	if err != nil {
		t.Fatal(err)
	}

	if err := s.AddMRComment(mr1.ID, uid, "hi", "md"); err != nil {
		t.Fatal(err)
	}
	if err := s.AddMRSystemComment(mr1.ID, uid, "merged"); err != nil {
		t.Fatal(err)
	}
	rootID, err := s.AddDiffComment(mr1.ID, uid, "abc123", "file.txt", "new", 1, 0, "root", 0, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddDiffComment(mr1.ID, uid, "abc123", "file.txt", "new", 1, 0, "reply", rootID, false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddDiffComment(mr1.ID, uid, "abc123", "file.txt", "new", 2, 0, "pending root", 0, true); err != nil {
		t.Fatal(err)
	}

	counts, err := s.MRCommentCounts(repoID, []int64{mr1.ID, mr2.ID})
	if err != nil {
		t.Fatal(err)
	}
	if counts[mr1.ID] != 2 {
		t.Fatalf("mr1 count = %d, want 2 (1 comment + 1 diff root)", counts[mr1.ID])
	}
	if counts[mr2.ID] != 0 {
		t.Fatalf("mr2 count = %d, want 0", counts[mr2.ID])
	}

	if empty, err := s.MRCommentCounts(repoID, nil); err != nil || len(empty) != 0 {
		t.Fatalf("MRCommentCounts(nil) = %v, %v", empty, err)
	}
}

// The list batches return what the per-item calls return, keyed by item.
func TestListPageBatches(t *testing.T) {
	s, repoID, uid := mrFixture(t)
	mr1, err := s.MRByNumber(repoID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AddMRReview(mr1.ID, uid, "approve", "abc123"); err != nil {
		t.Fatal(err)
	}
	reviews, err := s.MRReviewsFor([]int64{mr1.ID, mr1.ID + 99})
	if err != nil {
		t.Fatal(err)
	}
	if got := reviews[mr1.ID]; len(got) != 1 || got[0].Reviewer != "cmc" || got[0].Verdict != "approve" {
		t.Errorf("reviews = %+v", got)
	}
	if len(reviews[mr1.ID+99]) != 0 {
		t.Errorf("reviews for a missing MR: %+v", reviews[mr1.ID+99])
	}

	if err := s.SetCommitStatus(repoID, "abc123", "ci/test", "failure", "", "", uid); err != nil {
		t.Fatal(err)
	}
	if err := s.SetCommitStatus(repoID, "abc123", "ci/build", "success", "", "", uid); err != nil {
		t.Fatal(err)
	}
	statuses, err := s.CommitStatusesFor(repoID, []string{"abc123", "def456"})
	if err != nil {
		t.Fatal(err)
	}
	if got := statuses["abc123"]; len(got) != 2 || got[0].Context != "ci/build" || got[1].State != "failure" {
		t.Errorf("statuses = %+v", got)
	}
	if len(statuses["def456"]) != 0 {
		t.Errorf("statuses for a commit with none: %+v", statuses["def456"])
	}

	n, err := s.CreateIssue(repoID, uid, "t", "", "md")
	if err != nil {
		t.Fatal(err)
	}
	issue, err := s.IssueByNumber(repoID, n)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AddIssueComment(issue.ID, uid, "hi", "md"); err != nil {
		t.Fatal(err)
	}
	if err := s.AddIssueSystemComment(issue.ID, uid, "closed"); err != nil {
		t.Fatal(err)
	}
	counts, err := s.IssueCommentCounts([]int64{issue.ID})
	if err != nil {
		t.Fatal(err)
	}
	if counts[issue.ID] != 1 {
		t.Errorf("comment count = %d, want 1", counts[issue.ID])
	}
}

func TestLabelAndRequestBatches(t *testing.T) {
	s, repoID, uid := mrFixture(t)
	mr, err := s.MRByNumber(repoID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetMRReviewRequest(mr.ID, uid, true); err != nil {
		t.Fatal(err)
	}
	reqs, err := s.MRReviewRequestsFor([]int64{mr.ID})
	if err != nil {
		t.Fatal(err)
	}
	if got := reqs[mr.ID]; len(got) != 1 || got[0] != "cmc" {
		t.Errorf("review requests = %v", got)
	}
	n, err := s.CreateIssue(repoID, uid, "t", "", "md")
	if err != nil {
		t.Fatal(err)
	}
	issue, err := s.IssueByNumber(repoID, n)
	if err != nil {
		t.Fatal(err)
	}
	labels, assignees, err := s.IssueLabelsFor([]int64{issue.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(labels[issue.ID]) != 0 || len(assignees[issue.ID]) != 0 {
		t.Errorf("fresh issue: labels %v, assignees %v", labels[issue.ID], assignees[issue.ID])
	}
}
