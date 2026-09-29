package store

import (
	"slices"
	"testing"
)

// A queued merge rides on the merge request row, re-queueing replaces
// the queuer and strategy and clears the reason, and dequeueing says
// whether there was anything to take off.
func TestMergeQueueRoundTrip(t *testing.T) {
	s, repoID, uid := mrFixture(t)
	mr, err := s.MRByNumber(repoID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if mr.QueuedAt != "" {
		t.Fatalf("fresh MR is queued: %+v", mr)
	}
	if err := s.QueueMerge(mr.ID, uid, "ff"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetMergeQueueReason(mr.ID, "checks pending"); err != nil {
		t.Fatal(err)
	}
	mr, _ = s.MRByNumber(repoID, 1)
	if mr.QueuedAt == "" || mr.QueuedBy != "cmc" || mr.QueuedByID != uid || mr.QueueStrategy != "ff" || mr.QueueReason != "checks pending" {
		t.Fatalf("queued MR: %+v", mr)
	}
	byID, err := s.MRByID(mr.ID)
	if err != nil || byID.Number != 1 || byID.QueueStrategy != "ff" {
		t.Fatalf("MRByID = %+v, %v", byID, err)
	}

	if err := s.QueueMerge(mr.ID, uid, "merge"); err != nil {
		t.Fatal(err)
	}
	mr, _ = s.MRByNumber(repoID, 1)
	if mr.QueueStrategy != "merge" || mr.QueueReason != "" {
		t.Fatalf("re-queued MR: %+v", mr)
	}

	ids, err := s.QueuedMRsAtHead(repoID, "abc123")
	if err != nil || !slices.Equal(ids, []int64{mr.ID}) {
		t.Fatalf("QueuedMRsAtHead = %v, %v", ids, err)
	}
	if ids, _ := s.QueuedMRsAtHead(repoID, "other"); len(ids) != 0 {
		t.Fatalf("QueuedMRsAtHead(other) = %v", ids)
	}

	if ok, err := s.DequeueMerge(mr.ID); err != nil || !ok {
		t.Fatalf("DequeueMerge = %v, %v", ok, err)
	}
	if ok, _ := s.DequeueMerge(mr.ID); ok {
		t.Fatal("second DequeueMerge found a row")
	}
}

// A merge request that leaves the open states leaves the queue with it,
// whichever path merged or closed it.
func TestMergeQueueLeftOnMergeOrClose(t *testing.T) {
	s, repoID, uid := mrFixture(t)
	mr, _ := s.MRByNumber(repoID, 1)
	for _, mark := range []func() error{
		func() error { return s.MarkMerged(mr.ID, "base", uid, "") },
		func() error { return s.MarkClosed(mr.ID, uid, "") },
	} {
		if err := s.SetMRState(mr.ID, "open"); err != nil {
			t.Fatal(err)
		}
		if err := s.QueueMerge(mr.ID, uid, ""); err != nil {
			t.Fatal(err)
		}
		if err := mark(); err != nil {
			t.Fatal(err)
		}
		got, _ := s.MRByNumber(repoID, 1)
		if got.QueuedAt != "" {
			t.Fatalf("%s MR still queued: %+v", got.State, got)
		}
	}
	// source_gone keeps it: the branch can come back.
	if err := s.SetMRState(mr.ID, "open"); err != nil {
		t.Fatal(err)
	}
	s.QueueMerge(mr.ID, uid, "")
	s.SetMRState(mr.ID, "source_gone")
	if got, _ := s.MRByNumber(repoID, 1); got.QueuedAt == "" {
		t.Fatal("source_gone dropped the queued merge")
	}
}
