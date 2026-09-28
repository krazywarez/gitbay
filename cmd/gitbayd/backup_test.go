package main

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"filippo.io/age"

	"gitbay.org/gitbay/internal/backuplock"
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

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@e",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@e")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// verify runs git's connectivity check on every repository the
// database names: a repository missing an object fails it.
func TestVerifyChecksConnectivity(t *testing.T) {
	cfg := testConfig(t)
	st, err := openStore(cfg)
	if err != nil {
		t.Fatal(err)
	}
	uid, err := st.CreateUser("krz", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateRepo("user", uid, "thing", "public"); err != nil {
		t.Fatal(err)
	}
	st.Close()

	work := t.TempDir()
	gitIn(t, work, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(work, "a.txt"), []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, work, "add", "a.txt")
	gitIn(t, work, "commit", "-q", "-m", "one")
	dir := filepath.Join(cfg.Server.Root, "repos", "krz", "thing.git")
	gitIn(t, work, "clone", "-q", "--bare", work, dir)

	good := filepath.Join(t.TempDir(), "good.tar.gz")
	if err := runBackup(cfg, good, false); err != nil {
		t.Fatal(err)
	}
	if err := verifyBackup(good, ""); err != nil {
		t.Fatalf("intact archive: %v", err)
	}

	blob := gitIn(t, dir, "rev-parse", "HEAD:a.txt")
	if err := os.Remove(filepath.Join(dir, "objects", blob[:2], blob[2:])); err != nil {
		t.Fatal(err)
	}
	bad := filepath.Join(t.TempDir(), "bad.tar.gz")
	if err := runBackup(cfg, bad, false); err != nil {
		t.Fatal(err)
	}
	err = verifyBackup(bad, "")
	if err == nil || !strings.Contains(err.Error(), "krz/thing") || !strings.Contains(err.Error(), "connectivity") {
		t.Fatalf("archive with a missing blob: %v", err)
	}
}

// A full backup waits for a delete under way, and does not archive its
// own lock file.
func TestFullBackupWaitsForRepositoryMoves(t *testing.T) {
	cfg := testConfig(t)
	s, err := openStore(cfg)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	inFlight, err := backuplock.TryShared(cfg.Server.Root)
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "b.tar.gz")
	done := make(chan error, 1)
	go func() { done <- runBackup(cfg, out, false) }()
	select {
	case err := <-done:
		t.Fatalf("backup finished while a delete held the lock: %v", err)
	case <-time.After(200 * time.Millisecond):
	}
	inFlight()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("backup never started after the delete finished")
	}
	for _, n := range members(t, out) {
		if n == backuplock.Name {
			t.Fatalf("archive carries %s", n)
		}
	}
}

// A repository with every ref packed keeps its empty refs/heads and
// refs/tags directories through backup and extraction, the same as a real
// restore would: git needs refs/ to recognize a bare repository at all,
// even when every ref lives in packed-refs (#259).
func TestBackupPreservesPackedRefDirs(t *testing.T) {
	cfg := testConfig(t)
	st, err := openStore(cfg)
	if err != nil {
		t.Fatal(err)
	}
	uid, err := st.CreateUser("krz", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateRepo("user", uid, "thing", "public"); err != nil {
		t.Fatal(err)
	}
	st.Close()

	work := t.TempDir()
	gitIn(t, work, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(work, "a.txt"), []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, work, "add", "a.txt")
	gitIn(t, work, "commit", "-q", "-m", "one")
	dir := filepath.Join(cfg.Server.Root, "repos", "krz", "thing.git")
	gitIn(t, work, "clone", "-q", "--bare", work, dir)
	gitIn(t, dir, "pack-refs", "--all")
	entries, err := os.ReadDir(filepath.Join(dir, "refs", "heads"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("refs/heads not empty after pack-refs --all: %v", entries)
	}

	archive := filepath.Join(t.TempDir(), "b.tar.gz")
	if err := runBackup(cfg, archive, false); err != nil {
		t.Fatal(err)
	}

	// verify sees the archive exactly as a restore would: no workaround.
	if err := verifyBackup(archive, ""); err != nil {
		t.Fatalf("verify: %v", err)
	}

	restored := t.TempDir()
	if out, err := exec.Command("tar", "-xzf", archive, "-C", restored).CombinedOutput(); err != nil {
		t.Fatalf("extract: %v\n%s", err, out)
	}
	restoredRepo := filepath.Join(restored, "repos", "krz", "thing.git")
	if got := gitIn(t, restoredRepo, "rev-parse", "--verify", "HEAD"); got == "" {
		t.Fatal("rev-parse --verify HEAD returned nothing after restore")
	}
	gitIn(t, restoredRepo, "fsck", "--connectivity-only", "--no-progress", "--no-dangling")
}

// A commit pushed after a repository's refs are archived and before its
// objects are leaves the archive with the earlier refs and every object
// they reach, plus the new ones unreferenced (#259).
func TestBackupArchivesRefsBeforeObjects(t *testing.T) {
	cfg := testConfig(t)
	st, err := openStore(cfg)
	if err != nil {
		t.Fatal(err)
	}
	uid, err := st.CreateUser("krz", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateRepo("user", uid, "thing", "public"); err != nil {
		t.Fatal(err)
	}
	st.Close()

	work := t.TempDir()
	gitIn(t, work, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(work, "a.txt"), []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, work, "add", "a.txt")
	gitIn(t, work, "commit", "-q", "-m", "one")
	dir := filepath.Join(cfg.Server.Root, "repos", "krz", "thing.git")
	gitIn(t, work, "clone", "-q", "--bare", work, dir)
	first := gitIn(t, dir, "rev-parse", "refs/heads/main")

	var second string
	afterRefs = func(repo string) {
		if repo != dir {
			return
		}
		if err := os.WriteFile(filepath.Join(work, "b.txt"), []byte("b\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		gitIn(t, work, "add", "b.txt")
		gitIn(t, work, "commit", "-q", "-m", "two")
		gitIn(t, work, "push", "-q", dir, "main")
		second = gitIn(t, dir, "rev-parse", "refs/heads/main")
	}
	t.Cleanup(func() { afterRefs = func(string) {} })

	archive := filepath.Join(t.TempDir(), "b.tar.gz")
	if err := runBackup(cfg, archive, false); err != nil {
		t.Fatal(err)
	}
	if second == "" || second == first {
		t.Fatal("the push between the refs and the objects did not happen")
	}
	if err := verifyBackup(archive, ""); err != nil {
		t.Fatalf("verify: %v", err)
	}
	restored := t.TempDir()
	if out, err := exec.Command("tar", "-xzf", archive, "-C", restored).CombinedOutput(); err != nil {
		t.Fatalf("extract: %v\n%s", err, out)
	}
	repo := filepath.Join(restored, "repos", "krz", "thing.git")
	if got := gitIn(t, repo, "rev-parse", "refs/heads/main"); got != first {
		t.Errorf("archived main is %s, want %s from before the push", got, first)
	}
	gitIn(t, repo, "cat-file", "-e", second)
}

// A pack removed between the walk listing it and reading it is skipped,
// and verify's fsck then reports what it held; a vanished file outside
// objects/ still fails the backup.
func TestBackupSkipsVanishedObjects(t *testing.T) {
	cfg := testConfig(t)
	st, err := openStore(cfg)
	if err != nil {
		t.Fatal(err)
	}
	uid, err := st.CreateUser("krz", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateRepo("user", uid, "thing", "public"); err != nil {
		t.Fatal(err)
	}
	st.Close()

	work := t.TempDir()
	gitIn(t, work, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(work, "a.txt"), []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, work, "add", "a.txt")
	gitIn(t, work, "commit", "-q", "-m", "one")
	dir := filepath.Join(cfg.Server.Root, "repos", "krz", "thing.git")
	gitIn(t, work, "clone", "-q", "--bare", work, dir)
	gitIn(t, dir, "repack", "-q", "-a", "-d")

	removed := ""
	beforeAdd = func(path string) {
		if removed == "" && strings.HasSuffix(path, ".pack") {
			removed = path
			os.Remove(path)
		}
	}
	t.Cleanup(func() { beforeAdd = func(string) {} })
	archive := filepath.Join(t.TempDir(), "b.tar.gz")
	if err := runBackup(cfg, archive, false); err != nil {
		t.Fatalf("backup with a vanished pack: %v", err)
	}
	if removed == "" {
		t.Fatal("no pack was archived")
	}
	for _, n := range members(t, archive) {
		if strings.HasSuffix(n, ".pack") {
			t.Errorf("archive carries %s", n)
		}
	}
	if err := verifyBackup(archive, ""); err == nil || !strings.Contains(err.Error(), "connectivity") {
		t.Errorf("verify of an archive missing its pack: %v", err)
	}

	beforeAdd = func(path string) {
		if strings.HasSuffix(path, filepath.Join("thing.git", "config")) {
			os.Remove(path)
		}
	}
	err = runBackup(cfg, filepath.Join(t.TempDir(), "c.tar.gz"), false)
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("backup with a vanished config: %v, want not-exist", err)
	}
}

// gc refuses while a full backup holds the lock.
func TestGCRefusedDuringBackup(t *testing.T) {
	cfg := testConfig(t)
	release, err := backuplock.Hold(cfg.Server.Root)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if err := runGC(cfg, "", false, false); !errors.Is(err, backuplock.ErrBusy) {
		t.Fatalf("gc during a backup: %v", err)
	}
}

// A backup and its verify need no key file: sealed values are copied as
// they are. A missing database is refused rather than created.
func TestBackupNeedsNoKeyFile(t *testing.T) {
	cfg := testConfig(t)
	out := filepath.Join(t.TempDir(), "b.tar.gz")
	if err := runBackup(cfg, out, false); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("backup without a database: %v", err)
	}
	if _, err := os.Stat(filepath.Join(cfg.Server.Root, "gitbay.db")); !os.IsNotExist(err) {
		t.Fatalf("backup created a database: %v", err)
	}
	s, err := openStore(cfg)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	if err := os.Remove(cfg.Server.SecretKeyFile); err != nil {
		t.Fatal(err)
	}
	if err := runBackup(cfg, out, false); err != nil {
		t.Fatalf("backup without the key file: %v", err)
	}
	if err := verifyBackup(out, ""); err != nil {
		t.Fatalf("verify without the key file: %v", err)
	}
}

// A run removes what a killed run left beside the archive once it is a
// day old, and leaves younger ones, which may belong to a run under way.
func TestBackupRemovesStaleTemporaries(t *testing.T) {
	cfg := testConfig(t)
	s, err := openStore(cfg)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	dir := t.TempDir()
	old := time.Now().Add(-25 * time.Hour)
	mk := func(name string, isDir bool, mtime time.Time) {
		p := filepath.Join(dir, name)
		if isDir {
			if err := os.Mkdir(p, 0o700); err != nil {
				t.Fatal(err)
			}
		} else if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, mtime, mtime); err != nil {
			t.Fatal(err)
		}
	}
	mk(".gitbay-snap-old", true, old)
	mk(".b.tar.gz.tmp-old", false, old)
	mk(".gitbay-snap-new", true, time.Now())
	mk(".b.tar.gz.tmp-new", false, time.Now())
	mk(".keep", false, old)
	if err := runBackup(cfg, filepath.Join(dir, "b.tar.gz"), true); err != nil {
		t.Fatal(err)
	}
	got := leftovers(t, dir)
	want := []string{".b.tar.gz.tmp-new", ".gitbay-snap-new", ".keep"}
	sort.Strings(got)
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("left %v, want %v", got, want)
	}
}

// An archive written under server.root, directly or through a symlink,
// would be in the next full backup, so it is refused.
func TestBackupRefusesOutputInsideRoot(t *testing.T) {
	cfg := testConfig(t)
	s, err := openStore(cfg)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(cfg.Server.Root, link); err != nil {
		t.Fatal(err)
	}
	for _, out := range []string{
		filepath.Join(cfg.Server.Root, "b.tar.gz"),
		filepath.Join(cfg.Server.Root, "backups", "b.tar.gz"),
		filepath.Join(link, "b.tar.gz"),
	} {
		if err := runBackup(cfg, out, true); err == nil || !strings.Contains(err.Error(), "inside server.root") {
			t.Errorf("%s: %v", out, err)
		}
	}
}
