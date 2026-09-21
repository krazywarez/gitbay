package store

import "testing"

// The profile's activity log has to count what the graph over it counts.
// For a user that is what they did, wherever they did it — not what
// happened on the repositories they happen to own.
func TestUserPublicEventsKeyOnTheActor(t *testing.T) {
	s := open(t)
	if err := s.MigrateUp(); err != nil {
		t.Fatal(err)
	}

	alice, err := s.CreateUser("alice", false)
	if err != nil {
		t.Fatal(err)
	}
	bob, err := s.CreateUser("bob", false)
	if err != nil {
		t.Fatal(err)
	}
	// alice owns one public and one private repository; bob owns one.
	aliceRepo, err := s.CreateRepo("user", alice, "app", "public")
	if err != nil {
		t.Fatal(err)
	}
	secret, err := s.CreateRepo("user", alice, "secret", "private")
	if err != nil {
		t.Fatal(err)
	}
	bobRepo, err := s.CreateRepo("user", bob, "tool", "public")
	if err != nil {
		t.Fatal(err)
	}

	for _, e := range []struct {
		repo, actor int64
		kind        string
	}{
		{aliceRepo, alice, "issue.opened"}, // hers, on her repo
		{bobRepo, alice, "issue.opened"},   // hers, on someone else's
		{aliceRepo, bob, "issue.opened"},   // his, on her repo
		{secret, alice, "issue.opened"},    // hers, but private
		{aliceRepo, alice, "push"},         // pushes never appear
	} {
		if err := s.RecordEvent(e.repo, e.actor, e.kind, "{}"); err != nil {
			t.Fatal(err)
		}
	}

	got, err := s.UserPublicEvents(alice, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("want alice's two public events, got %d: %+v", len(got), got)
	}
	// Newest first, and both are hers.
	if got[0].RepoPath != "bob/tool" || got[1].RepoPath != "alice/app" {
		t.Errorf("wrong rows or order: %+v", got)
	}
	for _, e := range got {
		if e.Actor != "alice" {
			t.Errorf("an event alice did not do: %+v", e)
		}
	}

	if got, err := s.UserPublicEvents(alice, 1); err != nil || len(got) != 1 {
		t.Fatalf("limit not applied: %d %v", len(got), err)
	}
}
