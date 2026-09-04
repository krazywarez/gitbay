package store

import "testing"

func inboxFixture(t *testing.T) (s *Store, repoID, owner, other int64) {
	t.Helper()
	s = open(t)
	if err := s.MigrateUp(); err != nil {
		t.Fatal(err)
	}
	owner, err := s.CreateUser("cmc", true)
	if err != nil {
		t.Fatal(err)
	}
	other, err = s.CreateUser("kim", false)
	if err != nil {
		t.Fatal(err)
	}
	repoID, err = s.CreateRepo("user", owner, "lib", "public")
	if err != nil {
		t.Fatal(err)
	}
	return s, repoID, owner, other
}

func TestInboxUnreadAndRead(t *testing.T) {
	s, repoID, owner, _ := inboxFixture(t)
	for _, n := range []string{"opened issue #1", "commented on #1", "closed #1"} {
		if err := s.AddNotice(owner, repoID, "issue", "kim", n, "cmc/lib/issues/1"); err != nil {
			t.Fatal(err)
		}
	}
	if got := s.UnreadNotices(owner); got != 3 {
		t.Fatalf("unread = %d, want 3", got)
	}

	// Newest first, and the repo path is resolved from the polymorphic owner.
	got, err := s.Inbox(owner, true, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].Summary != "closed #1" || got[0].RepoPath != "cmc/lib" {
		t.Fatalf("inbox = %+v", got)
	}

	// Marking one read drops it from the unread list but not from --all.
	if n, err := s.MarkNoticesRead(owner, []int64{got[0].ID}); err != nil || n != 1 {
		t.Fatalf("MarkNoticesRead = %d, %v", n, err)
	}
	if got := s.UnreadNotices(owner); got != 2 {
		t.Fatalf("unread after read = %d, want 2", got)
	}
	all, _ := s.Inbox(owner, false, 0, 0)
	if len(all) != 3 || all[0].ReadAt == "" {
		t.Fatalf("all = %+v", all)
	}

	// The rest go in one sweep, and a second sweep changes nothing.
	if n, _ := s.MarkNoticesRead(owner, nil); n != 2 {
		t.Fatalf("sweep marked %d, want 2", n)
	}
	if n, _ := s.MarkNoticesRead(owner, nil); n != 0 {
		t.Fatalf("second sweep marked %d, want 0", n)
	}
}

// An id belonging to someone else matches nothing, so `notifications read
// <id>` cannot reach into another account's inbox.
func TestInboxIsPerUser(t *testing.T) {
	s, repoID, owner, other := inboxFixture(t)
	if err := s.AddNotice(owner, repoID, "issue", "kim", "opened issue #1", "cmc/lib/issues/1"); err != nil {
		t.Fatal(err)
	}
	mine, _ := s.Inbox(owner, true, 0, 0)
	if n, err := s.MarkNoticesRead(other, []int64{mine[0].ID}); err != nil || n != 0 {
		t.Fatalf("cross-user read marked %d rows (%v)", n, err)
	}
	if s.UnreadNotices(owner) != 1 {
		t.Fatal("another user's read cleared the owner's notice")
	}
	if got, _ := s.Inbox(other, true, 0, 0); len(got) != 0 {
		t.Fatalf("other user sees %+v", got)
	}
}

func TestInboxPaging(t *testing.T) {
	s, repoID, owner, _ := inboxFixture(t)
	for i := 0; i < 5; i++ {
		if err := s.AddNotice(owner, repoID, "issue", "kim", "note", "cmc/lib/issues/1"); err != nil {
			t.Fatal(err)
		}
	}
	first, err := s.Inbox(owner, true, 2, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 2 {
		t.Fatalf("first page %d rows, want 2", len(first))
	}
	next, err := s.Inbox(owner, true, 2, first[1].ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(next) != 2 || next[0].ID >= first[1].ID {
		t.Fatalf("second page %+v does not follow %+v", next, first)
	}
}

// Watching widens the recipients, muting removes a user who would
// otherwise be told, and the actor is never notified of their own action.
func TestNotifyRecipients(t *testing.T) {
	s, repoID, owner, other := inboxFixture(t)
	third, err := s.CreateUser("lee", false)
	if err != nil {
		t.Fatal(err)
	}

	got, err := s.NotifyRecipients(repoID, other, []int64{owner, other})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != owner {
		t.Fatalf("default recipients = %v, want [%d]", got, owner)
	}

	if err := s.SetRepoWatch(repoID, third, "watching"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.NotifyRecipients(repoID, other, []int64{owner}); len(got) != 2 {
		t.Fatalf("watcher not added: %v", got)
	}

	// A watcher who is also a target is listed once.
	if got, _ := s.NotifyRecipients(repoID, other, []int64{owner, third}); len(got) != 2 {
		t.Fatalf("watcher duplicated: %v", got)
	}

	// Muting beats owning the repository.
	if err := s.SetRepoWatch(repoID, owner, "muted"); err != nil {
		t.Fatal(err)
	}
	got, _ = s.NotifyRecipients(repoID, other, []int64{owner})
	if len(got) != 1 || got[0] != third {
		t.Fatalf("muted owner still notified: %v", got)
	}
	if s.RepoWatchState(repoID, owner) != "muted" {
		t.Fatal("watch state not recorded")
	}

	// Watching after muting replaces the row rather than adding one.
	if err := s.SetRepoWatch(repoID, owner, "watching"); err != nil {
		t.Fatal(err)
	}
	if s.RepoWatchState(repoID, owner) != "watching" {
		t.Fatal("watch did not replace mute")
	}
}
