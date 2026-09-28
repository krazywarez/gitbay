package main

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"filippo.io/age"

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
	cfg := testConfig(t)
	root := cfg.Server.Root
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

func TestBackupEncryptedToAgeRecipient(t *testing.T) {
	cfg := testConfig(t)
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Backup.AgeRecipients = []string{id.Recipient().String()}
	s, err := openStore(cfg)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()

	out := filepath.Join(t.TempDir(), "b.tar.gz.age")
	if err := runBackup(cfg, out, true); err != nil {
		t.Fatal(err)
	}
	head := make([]byte, 22)
	f, err := os.Open(out)
	if err != nil {
		t.Fatal(err)
	}
	_, err = io.ReadFull(f, head)
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	if string(head) != "age-encryption.org/v1\n" {
		t.Fatalf("archive is not age-encrypted: %q", head)
	}

	if err := verifyBackup(out, ""); err == nil || !strings.Contains(err.Error(), "--identity") {
		t.Fatalf("verify without an identity: %v", err)
	}
	idFile := filepath.Join(t.TempDir(), "backup-identity.txt")
	if err := os.WriteFile(idFile, []byte(id.String()+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := verifyBackup(out, idFile); err != nil {
		t.Fatalf("verify with the identity: %v", err)
	}
	other, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	otherFile := filepath.Join(t.TempDir(), "other.txt")
	if err := os.WriteFile(otherFile, []byte(other.String()+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var noMatch *age.NoIdentityMatchError
	if err := verifyBackup(out, otherFile); !errors.As(err, &noMatch) {
		t.Fatalf("verify with another identity: %v, want a no-identity-match error", err)
	}
}

// leftovers lists what a backup run left in dir besides the archive.
func leftovers(t *testing.T, dir string) []string {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), ".") {
			names = append(names, e.Name())
		}
	}
	return names
}

// The snapshot directory and the archive's temporary file are removed
// whether the run succeeds or fails, and a failed run leaves no archive.
func TestBackupLeavesNoTemporaries(t *testing.T) {
	cfg := testConfig(t)
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Backup.AgeRecipients = []string{id.Recipient().String()}
	s, err := openStore(cfg)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()

	dir := t.TempDir()
	out := filepath.Join(dir, "ok.tar.gz.age")
	if err := runBackup(cfg, out, false); err != nil {
		t.Fatal(err)
	}
	if got := leftovers(t, dir); len(got) != 0 {
		t.Errorf("after a successful run: %v", got)
	}
	fi, err := os.Stat(out)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("archive mode %v, want 0600", fi.Mode().Perm())
	}

	// A file the walk cannot read fails the run after the snapshot and
	// the temporary archive exist. Root reads a mode-0 file, so the case
	// needs an unprivileged user.
	if os.Geteuid() == 0 {
		t.Log("running as root: skipping the mid-walk failure case")
	} else {
		unreadable := filepath.Join(cfg.Server.Root, "unreadable")
		if err := os.WriteFile(unreadable, []byte("x"), 0o000); err != nil {
			t.Fatal(err)
		}
		failed := filepath.Join(dir, "failed.tar.gz.age")
		err := runBackup(cfg, failed, false)
		os.Remove(unreadable)
		if err == nil {
			t.Fatal("backup with an unreadable file succeeded")
		}
		if _, err := os.Stat(failed); !os.IsNotExist(err) {
			t.Errorf("failed run left an archive: %v", err)
		}
		if got := leftovers(t, dir); len(got) != 0 {
			t.Errorf("after a failed run: %v", got)
		}
	}

	bad := cfg
	bad.Backup.AgeRecipients = []string{"age1x"}
	if err := runBackup(bad, filepath.Join(dir, "bad.tar.gz.age"), true); err == nil {
		t.Fatal("backup with a bad recipient succeeded")
	}
	if got := leftovers(t, dir); len(got) != 0 {
		t.Errorf("after a bad recipient: %v", got)
	}
}

func TestBackupRefusesAgeNameWithoutRecipients(t *testing.T) {
	cfg := testConfig(t)
	out := filepath.Join(t.TempDir(), "b.tar.gz.age")
	err := runBackup(cfg, out, true)
	if err == nil || !strings.Contains(err.Error(), "age_recipients") {
		t.Fatalf("got %v, want a refusal naming age_recipients", err)
	}
}

// A truncated archive fails verification even when the tar stream's end
// markers survive: gzip's trailer and age's final chunk are checked.
func TestVerifyRejectsTruncatedArchive(t *testing.T) {
	cfg := testConfig(t)
	s, err := openStore(cfg)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	dir := t.TempDir()
	plain := filepath.Join(dir, "p.tar.gz")
	if err := runBackup(cfg, plain, true); err != nil {
		t.Fatal(err)
	}
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	enc := cfg
	enc.Backup.AgeRecipients = []string{id.Recipient().String()}
	sealed := filepath.Join(dir, "e.tar.gz.age")
	if err := runBackup(enc, sealed, true); err != nil {
		t.Fatal(err)
	}
	idFile := filepath.Join(dir, "id.txt")
	if err := os.WriteFile(idFile, []byte(id.String()+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := verifyBackup(plain, ""); err != nil {
		t.Fatalf("intact plain archive: %v", err)
	}
	if err := verifyBackup(sealed, idFile); err != nil {
		t.Fatalf("intact encrypted archive: %v", err)
	}

	for _, c := range []struct {
		src      string
		cut      int
		identity string
	}{
		{plain, 1, ""},
		{sealed, 1, idFile},
		{sealed, 100, idFile},
	} {
		data, err := os.ReadFile(c.src)
		if err != nil {
			t.Fatal(err)
		}
		short := filepath.Join(dir, "short-"+filepath.Base(c.src))
		if err := os.WriteFile(short, data[:len(data)-c.cut], 0o600); err != nil {
			t.Fatal(err)
		}
		if err := verifyBackup(short, c.identity); err == nil {
			t.Errorf("%s cut by %d bytes verified", filepath.Base(c.src), c.cut)
		}
	}
}

func TestArchivePath(t *testing.T) {
	now := time.Date(2026, 9, 27, 9, 0, 0, 0, time.UTC)
	plain := testConfig(t)
	enc := plain
	enc.Backup.AgeRecipients = []string{"age1x"}
	for _, c := range []struct {
		out  string
		cfg  config.Config
		want string
	}{
		{"", plain, "gitbay-backup-20260927-090000.tar.gz"},
		{"", enc, "gitbay-backup-20260927-090000.tar.gz.age"},
		{"/b/x.tar.gz", enc, "/b/x.tar.gz.age"},
		{"/b/x.tar.gz.age", enc, "/b/x.tar.gz.age"},
		{"/b/x.tar.gz", plain, "/b/x.tar.gz"},
	} {
		if got := archivePath(c.out, c.cfg, now); got != c.want {
			t.Errorf("archivePath(%q) = %q, want %q", c.out, got, c.want)
		}
	}
}
