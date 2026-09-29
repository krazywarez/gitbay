package main

import (
	"bytes"
	"log/slog"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"gitbay.org/gitbay/internal/store"
)

// A restart that moves the schema says so. Migrations used to run in silence,
// which left an unexpected user_version with nothing in the journal tying it to
// the deploy that applied it.
func TestOpenStoreLogsSchemaMigration(t *testing.T) {
	cfg := testConfig(t)

	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	defer slog.SetDefault(prev)

	// First open creates the database and migrates it from nothing.
	s, err := openStore(cfg)
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	version, err := s.Version()
	if err != nil {
		t.Fatalf("version: %v", err)
	}
	s.Close()

	logged := buf.String()
	if !strings.Contains(logged, "schema migrated") {
		t.Fatalf("no migration logged:\n%s", logged)
	}
	for _, want := range []string{"from=0", "to=" + strconv.Itoa(version)} {
		if !strings.Contains(logged, want) {
			t.Errorf("expected %q in the log line:\n%s", want, logged)
		}
	}

	// Reopening an already-current database is silent: every restart would
	// otherwise claim a migration that did not happen.
	buf.Reset()
	s, err = openStore(cfg)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	s.Close()
	if strings.Contains(buf.String(), "schema migrated") {
		t.Errorf("logged a migration on an up-to-date database:\n%s", buf.String())
	}
}

// A note a migration leaves goes in the log line and is then dropped.
func TestOpenStoreLogsMigrationNote(t *testing.T) {
	cfg := testConfig(t)
	st, err := store.Open(filepath.Join(cfg.Server.Root, "gitbay.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.MigrateTo(1); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB.Exec("INSERT INTO settings (key, value) VALUES ('migration_note', 'removed 2 things')"); err != nil {
		t.Fatal(err)
	}
	st.Close()

	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	defer slog.SetDefault(prev)
	s, err := openStore(cfg)
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	defer s.Close()
	if !strings.Contains(buf.String(), `note="removed 2 things"`) {
		t.Fatalf("note not logged:\n%s", buf.String())
	}
	if note, err := s.TakeMigrationNote(); err != nil || note != "" {
		t.Fatalf("note left behind: %q, %v", note, err)
	}
}
