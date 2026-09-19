package store

import "testing"

// A new account follows the system scheme; a set value reads back.
func TestTheme(t *testing.T) {
	s := open(t)
	if err := s.MigrateUp(); err != nil {
		t.Fatal(err)
	}
	uid, err := s.CreateUser("cmc", false)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := s.Theme(uid); err != nil || got != "system" {
		t.Fatalf("default theme: %q %v", got, err)
	}
	if err := s.SetTheme(uid, "dark"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Theme(uid); got != "dark" {
		t.Fatalf("theme after set: %q", got)
	}
	if _, err := s.Theme(uid + 1); err != ErrNotFound {
		t.Fatalf("missing user: %v", err)
	}
}
