package main

import (
	"archive/tar"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"gitbay.org/gitbay/internal/config"
)

// members lists the archive's entries by name.
func members(t *testing.T, path string) []string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, hdr.Name)
	}
	sort.Strings(names)
	return names
}

// --db-only is what makes an hourly schedule affordable, so it has to leave
// the repositories out and still carry a restorable database.
func TestBackupDBOnlyOmitsRepositories(t *testing.T) {
	root := t.TempDir()
	cfg := config.Config{Server: config.Server{Root: root}}
	s, err := openStore(cfg)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()

	repo := filepath.Join(root, "repos", "krz", "thing.git")
	if err := os.MkdirAll(repo, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "HEAD"), []byte("ref: refs/heads/main\n"), 0o640); err != nil {
		t.Fatal(err)
	}

	full := filepath.Join(t.TempDir(), "full.tar.gz")
	if err := runBackup(cfg, full, false); err != nil {
		t.Fatalf("full backup: %v", err)
	}
	dbOnly := filepath.Join(t.TempDir(), "db.tar.gz")
	if err := runBackup(cfg, dbOnly, true); err != nil {
		t.Fatalf("db-only backup: %v", err)
	}

	fullNames := members(t, full)
	if len(fullNames) < 2 {
		t.Fatalf("full backup carries only %v", fullNames)
	}
	var sawRepo bool
	for _, n := range fullNames {
		if n == "repos/krz/thing.git/HEAD" {
			sawRepo = true
		}
	}
	if !sawRepo {
		t.Errorf("full backup is missing the repository: %v", fullNames)
	}

	if got := members(t, dbOnly); len(got) != 1 || got[0] != "gitbay.db" {
		t.Errorf("db-only backup carries %v, want [gitbay.db]", got)
	}

	fi, err := os.Stat(dbOnly)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Size() == 0 {
		t.Error("db-only backup is empty")
	}
}
