package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func open(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "gitbay.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestMigrateUpDown(t *testing.T) {
	s := open(t)

	if err := s.MigrateUp(); err != nil {
		t.Fatal(err)
	}
	v, err := s.Version()
	if err != nil {
		t.Fatal(err)
	}
	if v < 1 {
		t.Fatalf("version %d after MigrateUp", v)
	}

	// Seeded settings row exists.
	var epoch string
	if err := s.DB.QueryRow("SELECT value FROM settings WHERE key = 'key_epoch'").Scan(&epoch); err != nil {
		t.Fatal(err)
	}
	if epoch != "1" {
		t.Fatalf("key_epoch = %q, want 1", epoch)
	}

	// Down to empty, then back up.
	if err := s.MigrateTo(0); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := s.DB.QueryRow("SELECT count(*) FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("%d tables remain after down-migration to 0", n)
	}
	if err := s.MigrateUp(); err != nil {
		t.Fatal(err)
	}
	// Idempotent at latest.
	if err := s.MigrateUp(); err != nil {
		t.Fatal(err)
	}
}

func TestKeyFingerprintGloballyUnique(t *testing.T) {
	s := open(t)
	if err := s.MigrateUp(); err != nil {
		t.Fatal(err)
	}
	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := s.DB.Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}
	mustExec("INSERT INTO users (username) VALUES ('alice'), ('bob')")
	mustExec("INSERT INTO ssh_keys (user_id, fingerprint, algo, blob) VALUES (1, 'SHA256:aaa', 'ed25519', x'00')")

	// Same fingerprint on a different account must be rejected.
	_, err := s.DB.Exec("INSERT INTO ssh_keys (user_id, fingerprint, algo, blob) VALUES (2, 'SHA256:aaa', 'ed25519', x'00')")
	if err == nil || !strings.Contains(err.Error(), "UNIQUE") {
		t.Fatalf("duplicate ssh fingerprint across accounts: err = %v, want UNIQUE violation", err)
	}

	mustExec("INSERT INTO pgp_keys (user_id, fingerprint, armored) VALUES (1, 'FPR1', '-----')")
	_, err = s.DB.Exec("INSERT INTO pgp_keys (user_id, fingerprint, armored) VALUES (2, 'FPR1', '-----')")
	if err == nil || !strings.Contains(err.Error(), "UNIQUE") {
		t.Fatalf("duplicate pgp fingerprint across accounts: err = %v, want UNIQUE violation", err)
	}
}

func TestForeignKeysEnforced(t *testing.T) {
	s := open(t)
	if err := s.MigrateUp(); err != nil {
		t.Fatal(err)
	}
	_, err := s.DB.Exec("INSERT INTO ssh_keys (user_id, fingerprint, algo, blob) VALUES (999, 'SHA256:zzz', 'ed25519', x'00')")
	if err == nil {
		t.Fatal("insert with dangling user_id succeeded; foreign keys are off")
	}
}

// The database file carries token hashes, addresses and private repo names.
// The directory above it is the real boundary; this is the second one.
func TestDatabaseFileIsNotWorldReadable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gitbay.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if mode := fi.Mode().Perm(); mode&0o007 != 0 {
		t.Errorf("database mode %04o is other-readable", mode)
	}
}
