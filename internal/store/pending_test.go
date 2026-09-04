package store

import "testing"

func pendingFixture(t *testing.T) (*Store, int64, int64, int64) {
	t.Helper()
	s := open(t)
	if err := s.MigrateUp(); err != nil {
		t.Fatal(err)
	}
	author, err := s.CreateUser("cmc", true)
	if err != nil {
		t.Fatal(err)
	}
	other, err := s.CreateUser("kim", false)
	if err != nil {
		t.Fatal(err)
	}
	repoID, err := s.CreateRepo("user", author, "lib", "public")
	if err != nil {
		t.Fatal(err)
	}
	n, err := s.CreateMR(repoID, author, repoID, "feature", "main", "t", "", "abc123", "md", false)
	if err != nil {
		t.Fatal(err)
	}
	mr, err := s.MRByNumber(repoID, n)
	if err != nil {
		t.Fatal(err)
	}
	return s, mr.ID, author, other
}

// A pending comment belongs to the reviewer composing it and to nobody
// else, until they submit.
func TestPendingCommentsArePrivate(t *testing.T) {
	s, mrID, author, other := pendingFixture(t)
	if _, err := s.AddDiffComment(mrID, other, "abc123", "a.go", "new", 3, "half a thought", 0, true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddDiffComment(mrID, other, "abc123", "a.go", "new", 9, "said out loud", 0, false); err != nil {
		t.Fatal(err)
	}

	mine, err := s.ListDiffComments(mrID, other)
	if err != nil {
		t.Fatal(err)
	}
	if len(mine) != 2 {
		t.Fatalf("author of the pending comment sees %d, want both", len(mine))
	}
	theirs, _ := s.ListDiffComments(mrID, author)
	if len(theirs) != 1 || theirs[0].Body != "said out loud" {
		t.Fatalf("someone else sees %+v", theirs)
	}
	anon, _ := s.ListDiffComments(mrID, 0)
	if len(anon) != 1 {
		t.Fatalf("anonymous reader sees %d, want the published one only", len(anon))
	}
	if n := s.CountPendingComments(mrID, other); n != 1 {
		t.Fatalf("pending count = %d", n)
	}
}

// An unsubmitted thread must not gate a merge: nobody else can see it,
// so nobody else could resolve it.
func TestPendingThreadsDoNotBlockMerges(t *testing.T) {
	s, mrID, _, other := pendingFixture(t)
	if _, err := s.AddDiffComment(mrID, other, "abc123", "a.go", "new", 3, "pending", 0, true); err != nil {
		t.Fatal(err)
	}
	n, err := s.UnresolvedThreadCount(mrID)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("pending thread counted against the merge gate (%d)", n)
	}

	if _, err := s.PublishPendingComments(mrID, other); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.UnresolvedThreadCount(mrID); n != 1 {
		t.Fatalf("published thread does not gate (%d)", n)
	}
}

func TestPublishAndDiscardPending(t *testing.T) {
	s, mrID, author, other := pendingFixture(t)
	for i := 0; i < 3; i++ {
		if _, err := s.AddDiffComment(mrID, other, "abc123", "a.go", "new", int64(i+1), "note", 0, true); err != nil {
			t.Fatal(err)
		}
	}
	// Another reviewer's batch is untouched by either operation.
	if _, err := s.AddDiffComment(mrID, author, "abc123", "b.go", "new", 1, "mine", 0, true); err != nil {
		t.Fatal(err)
	}

	n, err := s.PublishPendingComments(mrID, other)
	if err != nil || n != 3 {
		t.Fatalf("published %d (%v), want 3", n, err)
	}
	if got := s.CountPendingComments(mrID, author); got != 1 {
		t.Fatalf("the other reviewer's batch was published too (%d left)", got)
	}
	// Publishing again is a no-op, not a double publish.
	if n, _ := s.PublishPendingComments(mrID, other); n != 0 {
		t.Fatalf("second publish moved %d rows", n)
	}

	// Discard removes only what is still pending.
	if n, _ := s.DiscardPendingComments(mrID, author); n != 1 {
		t.Fatalf("discarded %d, want 1", n)
	}
	all, _ := s.ListDiffComments(mrID, other)
	if len(all) != 3 {
		t.Fatalf("discard took published comments with it: %d remain", len(all))
	}
}
