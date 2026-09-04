package store

import "testing"

// A fork is created with its parent in the same insert; it is never a
// plain repository for a moment between two statements (#108).
func TestCreateForkSetsParent(t *testing.T) {
	s := open(t)
	if err := s.MigrateUp(); err != nil {
		t.Fatal(err)
	}
	uid, _ := s.CreateUser("alice", false)
	bob, _ := s.CreateUser("bob", false)
	parent, err := s.CreateRepo("user", uid, "app", "public")
	if err != nil {
		t.Fatal(err)
	}
	id, err := s.CreateFork("user", bob, "app", "public", parent)
	if err != nil {
		t.Fatal(err)
	}
	r, err := s.RepoByID(id)
	if err != nil || r.ForkOf != parent {
		t.Fatalf("fork_of = %d, want %d (err %v)", r.ForkOf, parent, err)
	}
	if _, err := s.CreateFork("user", bob, "app", "public", parent); err == nil {
		t.Fatal("duplicate fork name accepted")
	}
}
