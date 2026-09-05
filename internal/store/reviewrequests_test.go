package store

import "testing"

// A requested reviewer sees the merge request in their queue even with no
// other tie to the repository, and the queue empties once they have
// reviewed the current head — the two rules #145 requires together. A new
// push brings it back, and --remove drops it outright.
func TestReviewQueueRequestedReviewer(t *testing.T) {
	s, repoID, _ := mrFixture(t)
	mr, err := s.MRByNumber(repoID, 1)
	if err != nil {
		t.Fatal(err)
	}
	reviewerID, err := s.CreateUser("dana", false)
	if err != nil {
		t.Fatal(err)
	}

	if q, err := s.ReviewQueue(reviewerID); err != nil || len(q) != 0 {
		t.Fatalf("queue before any request: %+v, %v", q, err)
	}

	if err := s.SetMRReviewRequest(mr.ID, reviewerID, true); err != nil {
		t.Fatal(err)
	}
	q, err := s.ReviewQueue(reviewerID)
	if err != nil || len(q) != 1 || q[0].Number != mr.Number {
		t.Fatalf("requested reviewer not in queue: %+v, %v", q, err)
	}

	if err := s.AddMRReview(mr.ID, reviewerID, "approve", mr.HeadSHA); err != nil {
		t.Fatal(err)
	}
	if q, err := s.ReviewQueue(reviewerID); err != nil || len(q) != 0 {
		t.Fatalf("queue after reviewing the current head: %+v, %v", q, err)
	}

	if err := s.UpdateMRHead(mr.ID, "def456", ""); err != nil {
		t.Fatal(err)
	}
	if q, err := s.ReviewQueue(reviewerID); err != nil || len(q) != 1 {
		t.Fatalf("queue after a new head: %+v, %v", q, err)
	}

	if err := s.SetMRReviewRequest(mr.ID, reviewerID, false); err != nil {
		t.Fatal(err)
	}
	if q, err := s.ReviewQueue(reviewerID); err != nil || len(q) != 0 {
		t.Fatalf("queue after --remove: %+v, %v", q, err)
	}
	if err := s.SetMRReviewRequest(mr.ID, reviewerID, false); err != ErrNotFound {
		t.Fatalf("removing an absent request: %v", err)
	}
}

// The involved half of the queue never shows an author their own merge
// request (reviewQueueQuery's author_id <> ?1); the requested half must
// hold the same line even if the author is somehow added as a requested
// reviewer on their own MR.
func TestReviewQueueExcludesAuthor(t *testing.T) {
	s, repoID, authorID := mrFixture(t)
	mr, err := s.MRByNumber(repoID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetMRReviewRequest(mr.ID, authorID, true); err != nil {
		t.Fatal(err)
	}
	if q, err := s.ReviewQueue(authorID); err != nil || len(q) != 0 {
		t.Fatalf("author requested on their own MR should not see it in queue: %+v, %v", q, err)
	}
}
