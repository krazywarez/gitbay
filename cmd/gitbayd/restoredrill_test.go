package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A drill restores a full archive into an empty directory as a usable
// root, verifies it, and refuses a directory that already holds files.
func TestRestoreDrill(t *testing.T) {
	cfg := testConfig(t)
	root := cfg.Server.Root
	st, err := openStore(cfg)
	if err != nil {
		t.Fatal(err)
	}
	uid, err := st.CreateUser("krz", false)
	if err != nil {
		t.Fatal(err)
	}
	rid, err := st.CreateRepo("user", uid, "thing", "public")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateIssue(rid, uid, "bug", "", "md"); err != nil {
		t.Fatal(err)
	}
	if err := st.RecordEvent(rid, uid, "push", "{}"); err != nil {
		t.Fatal(err)
	}
	st.Close()
	work := t.TempDir()
	gitIn(t, work, "init", "-q", "-b", "main")
	writeFile(t, filepath.Join(work, "a.txt"), []byte("a\n"))
	gitIn(t, work, "add", "a.txt")
	gitIn(t, work, "commit", "-q", "-m", "one")
	gitIn(t, work, "clone", "-q", "--bare", work, filepath.Join(root, "repos", "krz", "thing.git"))
	writeFile(t, filepath.Join(root, "ssh", "host_ed25519"), []byte("key\n"))

	archive := filepath.Join(t.TempDir(), "b.tar.gz")
	if err := runBackup(cfg, archive, false); err != nil {
		t.Fatal(err)
	}
	into := filepath.Join(t.TempDir(), "restored")
	if err := restoreDrill(archive, "", into); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"gitbay.db", "ssh/host_ed25519", "repos/krz/thing.git/HEAD"} {
		if _, err := os.Stat(filepath.Join(into, p)); err != nil {
			t.Errorf("restored root lacks %s: %v", p, err)
		}
	}
	if got := gitIn(t, filepath.Join(into, "repos", "krz", "thing.git"), "log", "--format=%s", "main"); got != "one" {
		t.Errorf("restored log %q", got)
	}

	err = restoreDrill(archive, "", into)
	if err == nil || !strings.Contains(err.Error(), "not empty") {
		t.Fatalf("drill into a non-empty directory: %v", err)
	}
}
