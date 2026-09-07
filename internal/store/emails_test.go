package store

import (
	"errors"
	"testing"
	"time"
)

func emailFixture(t *testing.T) (*Store, int64) {
	t.Helper()
	s := open(t)
	if err := s.MigrateUp(); err != nil {
		t.Fatal(err)
	}
	uid, err := s.CreateUser("gus", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AddEmail(uid, "gus@primary.test", "smtp", true); err != nil {
		t.Fatal(err)
	}
	return s, uid
}

func hasEmail(t *testing.T, s *Store, uid int64, address string) bool {
	t.Helper()
	list, err := s.ListEmails(uid)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range list {
		if e.Address == address {
			return true
		}
	}
	return false
}

func epoch(t *testing.T, s *Store) int64 {
	t.Helper()
	v, err := s.KeyEpoch()
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// Removing an unverified address takes its pending verification code
// with it, and does not touch the key epoch: an unverified address was
// never a trust input.
func TestRemoveEmailUnverified(t *testing.T) {
	s, uid := emailFixture(t)
	if err := s.AddEmail(uid, "typo@example.test", "", false); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateEmailToken(uid, "typo@example.test", "hash1", time.Hour); err != nil {
		t.Fatal(err)
	}
	before := epoch(t, s)
	if err := s.RemoveEmail(uid, "typo@example.test"); err != nil {
		t.Fatal(err)
	}
	if hasEmail(t, s, uid, "typo@example.test") {
		t.Fatal("address still listed after removal")
	}
	if _, err := s.ConsumeEmailToken(uid, "hash1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("verification code survived the address: %v", err)
	}
	if got := epoch(t, s); got != before {
		t.Fatalf("key epoch moved %d -> %d for an unverified address", before, got)
	}
}

// A verified address is a trust input for signature states, so removing
// one invalidates the cache.
func TestRemoveEmailVerifiedBumpsKeyEpoch(t *testing.T) {
	s, uid := emailFixture(t)
	if err := s.AddEmail(uid, "old@example.test", "smtp", false); err != nil {
		t.Fatal(err)
	}
	before := epoch(t, s)
	if err := s.RemoveEmail(uid, "old@example.test"); err != nil {
		t.Fatal(err)
	}
	if got := epoch(t, s); got != before+1 {
		t.Fatalf("key epoch %d -> %d, want +1", before, got)
	}
}

func TestRemoveEmailRefusesPrimary(t *testing.T) {
	s, uid := emailFixture(t)
	if err := s.AddEmail(uid, "other@example.test", "smtp", false); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveEmail(uid, "gus@primary.test"); !errors.Is(err, ErrPrimaryEmail) {
		t.Fatalf("removing the primary: %v", err)
	}
	if !hasEmail(t, s, uid, "gus@primary.test") {
		t.Fatal("primary removed despite refusal")
	}
}

// Activation, login links and commit identity all resolve through
// verified addresses; the last one stays even when it is not primary.
func TestRemoveEmailRefusesLastVerified(t *testing.T) {
	s := open(t)
	if err := s.MigrateUp(); err != nil {
		t.Fatal(err)
	}
	uid, err := s.CreateUser("gus", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AddEmail(uid, "gus@primary.test", "", true); err != nil {
		t.Fatal(err)
	}
	if err := s.AddEmail(uid, "only@example.test", "smtp", false); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveEmail(uid, "only@example.test"); !errors.Is(err, ErrLastVerifiedEmail) {
		t.Fatalf("removing the only verified address: %v", err)
	}
	if !hasEmail(t, s, uid, "only@example.test") {
		t.Fatal("last verified address removed despite refusal")
	}
}

// An address on another account, or on none, is not found: the
// uniqueness of addresses must not let one account act on another's.
func TestRemoveEmailNotFound(t *testing.T) {
	s, uid := emailFixture(t)
	other, err := s.CreateUser("ada", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AddEmail(other, "ada@example.test", "", true); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveEmail(uid, "ada@example.test"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another account's address: %v", err)
	}
	if !hasEmail(t, s, other, "ada@example.test") {
		t.Fatal("another account's address was removed")
	}
	if err := s.RemoveEmail(uid, "nobody@example.test"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("absent address: %v", err)
	}
}

func TestSetPrimaryEmail(t *testing.T) {
	s, uid := emailFixture(t)
	if err := s.AddEmail(uid, "new@example.test", "smtp", false); err != nil {
		t.Fatal(err)
	}
	if err := s.AddEmail(uid, "pending@example.test", "", false); err != nil {
		t.Fatal(err)
	}
	if err := s.SetPrimaryEmail(uid, "pending@example.test"); !errors.Is(err, ErrUnverifiedEmail) {
		t.Fatalf("unverified address as primary: %v", err)
	}
	if err := s.SetPrimaryEmail(uid, "nobody@example.test"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("absent address as primary: %v", err)
	}
	if err := s.SetPrimaryEmail(uid, "new@example.test"); err != nil {
		t.Fatal(err)
	}
	list, err := s.ListEmails(uid)
	if err != nil {
		t.Fatal(err)
	}
	var primaries []string
	for _, e := range list {
		if e.Primary {
			primaries = append(primaries, e.Address)
		}
	}
	if len(primaries) != 1 || primaries[0] != "new@example.test" {
		t.Fatalf("primaries after change: %v", primaries)
	}
	if addr, _ := s.PrimaryVerifiedEmail(uid); addr != "new@example.test" {
		t.Fatalf("PrimaryVerifiedEmail after change: %q", addr)
	}
	// The old primary can go now.
	if err := s.RemoveEmail(uid, "gus@primary.test"); err != nil {
		t.Fatalf("removing the former primary: %v", err)
	}
}
