package store

import (
	"testing"
	"time"
)

func TestCountLoginTokensSince(t *testing.T) {
	s := open(t)
	if err := s.MigrateUp(); err != nil {
		t.Fatal(err)
	}
	uid, err := s.CreateUser("cmc", true)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		_, hash, err := NewToken()
		if err != nil {
			t.Fatal(err)
		}
		if err := s.CreateLoginToken(uid, hash, time.Minute); err != nil {
			t.Fatal(err)
		}
	}

	n, err := s.CountLoginTokensSince(uid, time.Now().Add(-time.Hour))
	if err != nil || n != 3 {
		t.Fatalf("count in the last hour = %d, %v; want 3", n, err)
	}

	// A window that opens in the future sees none of them, which is what
	// makes the hourly bound a window rather than a lifetime total.
	if n, err := s.CountLoginTokensSince(uid, time.Now().Add(time.Hour)); err != nil || n != 0 {
		t.Fatalf("count in a future window = %d, %v; want 0", n, err)
	}

	// One account's requests must not spend another account's budget.
	other, err := s.CreateUser("kim", false)
	if err != nil {
		t.Fatal(err)
	}
	if n, err := s.CountLoginTokensSince(other, time.Now().Add(-time.Hour)); err != nil || n != 0 {
		t.Fatalf("other account count = %d, %v; want 0", n, err)
	}
}
