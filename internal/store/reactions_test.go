package store

import (
	"errors"
	"testing"
)

func TestReactions(t *testing.T) {
	s, repoID, uid := mrFixture(t)
	bob, err := s.CreateUser("bob", false)
	if err != nil {
		t.Fatal(err)
	}
	num, err := s.CreateIssue(repoID, uid, "t", "b", "md")
	if err != nil {
		t.Fatal(err)
	}
	iss, err := s.IssueByNumber(repoID, num)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AddIssueComment(iss.ID, uid, "c", "md"); err != nil {
		t.Fatal(err)
	}
	if err := s.AddIssueSystemComment(iss.ID, uid, "sys"); err != nil {
		t.Fatal(err)
	}
	cs, _ := s.ListIssueComments(iss.ID)
	if len(cs) != 2 || cs[0].ID == 0 {
		t.Fatalf("comments: %+v", cs)
	}
	cid, sysID := cs[0].ID, cs[1].ID

	if err := s.ReactionTarget("issue", iss.ID, cid); err != nil {
		t.Fatal(err)
	}
	if err := s.ReactionTarget("issue", iss.ID, sysID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("system comment: %v", err)
	}
	if err := s.ReactionTarget("issue", iss.ID+1, cid); !errors.Is(err, ErrNotFound) {
		t.Fatalf("comment of another thread: %v", err)
	}

	for i := 0; i < 2; i++ { // idempotent
		if err := s.SetReaction("issue", iss.ID, 0, uid, "+1", true); err != nil {
			t.Fatal(err)
		}
		if err := s.SetReaction("issue", iss.ID, cid, uid, "+1", true); err != nil {
			t.Fatal(err)
		}
	}
	s.SetReaction("issue", iss.ID, 0, bob, "+1", true)
	s.SetReaction("issue", iss.ID, 0, bob, "eyes", true)
	got, err := s.ReactionCounts("issue", iss.ID, uid)
	if err != nil {
		t.Fatal(err)
	}
	if b := got[0]; len(b) != 2 || b[0] != (ReactionCount{"+1", 2, true}) || b[1] != (ReactionCount{"eyes", 1, false}) {
		t.Errorf("body: %+v", b)
	}
	if c := got[cid]; len(c) != 1 || c[0] != (ReactionCount{"+1", 1, true}) {
		t.Errorf("comment: %+v", c)
	}
	anon, _ := s.ReactionCounts("issue", iss.ID, 0)
	if anon[0][0].Me {
		t.Errorf("anonymous viewer marked: %+v", anon[0])
	}

	// Removing is scoped to the item and idempotent.
	for i := 0; i < 2; i++ {
		if err := s.SetReaction("issue", iss.ID, 0, uid, "+1", false); err != nil {
			t.Fatal(err)
		}
	}
	got, _ = s.ReactionCounts("issue", iss.ID, uid)
	if got[0][0] != (ReactionCount{"+1", 1, false}) || len(got[cid]) != 1 {
		t.Errorf("after remove: %+v", got)
	}

	// Deleting the issue takes its reactions with it.
	if _, err := s.DB.Exec("DELETE FROM issues WHERE id = ?", iss.ID); err != nil {
		t.Fatal(err)
	}
	var n int
	s.DB.QueryRow("SELECT COUNT(*) FROM issue_reactions").Scan(&n)
	if n != 0 {
		t.Errorf("%d reactions left after issue delete", n)
	}

	mr, _ := s.MRByNumber(repoID, 1)
	if err := s.SetReaction("mr", mr.ID, 0, uid, "rocket", true); err != nil {
		t.Fatal(err)
	}
	m, _ := s.ReactionCounts("mr", mr.ID, uid)
	if len(m[0]) != 1 || m[0][0].Reaction != "rocket" {
		t.Errorf("mr: %+v", m)
	}
	if err := s.SetReaction("issue", 1, 0, uid, "bogus", true); err == nil {
		t.Error("unknown reaction stored")
	}
}

func TestParseReaction(t *testing.T) {
	for in, want := range map[string]string{"+1": "+1", "👍": "+1", "❤️": "heart", "❤": "heart", "eyes": "eyes"} {
		if got, ok := ParseReaction(in); !ok || got != want {
			t.Errorf("%q: %q %v", in, got, ok)
		}
	}
	if _, ok := ParseReaction("thumbsup"); ok {
		t.Error("accepted an unknown name")
	}
}
