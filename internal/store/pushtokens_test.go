package store

import (
	"errors"
	"testing"
	"time"
)

func TestPushTokens(t *testing.T) {
	s := open(t)
	if err := s.MigrateUp(); err != nil {
		t.Fatal(err)
	}
	uid, err := s.CreateUser("alice", false)
	if err != nil {
		t.Fatal(err)
	}
	repoID, err := s.CreateRepo("user", uid, "app", "public")
	if err != nil {
		t.Fatal(err)
	}
	token, err := s.CreatePushToken(repoID, uid, "full")
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.PushTokenByHash(HashToken(token))
	if err != nil || got != (PushToken{RepoID: repoID, UserID: uid, Scope: "full"}) {
		t.Fatalf("lookup = %+v, %v", got, err)
	}
	if err := s.DeletePushToken(token); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PushTokenByHash(HashToken(token)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after delete: %v", err)
	}

	// A token whose receive-pack never cleaned up is swept after a day.
	stale, err := s.CreatePushToken(repoID, uid, "full")
	if err != nil {
		t.Fatal(err)
	}
	swept, err := s.Sweep(Retention{}, time.Now().Add(25*time.Hour))
	if err != nil || swept["push_tokens"] != 1 {
		t.Fatalf("sweep = %v, %v", swept, err)
	}
	if _, err := s.PushTokenByHash(HashToken(stale)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after sweep: %v", err)
	}
}
