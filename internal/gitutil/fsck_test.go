package gitutil

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestFsckConnectivityFindsAMissingObject(t *testing.T) {
	dir := t.TempDir()
	git(t, dir, "init", "-q", "-b", "main")
	write(t, dir, "a.txt", "a\n")
	git(t, dir, "add", "a.txt")
	git(t, dir, "commit", "-q", "-m", "one")
	if err := FsckConnectivity(dir); err != nil {
		t.Fatalf("intact repository: %v", err)
	}
	out, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD:a.txt").Output()
	if err != nil {
		t.Fatal(err)
	}
	blob := strings.TrimSpace(string(out))
	if err := os.Remove(filepath.Join(dir, ".git", "objects", blob[:2], blob[2:])); err != nil {
		t.Fatal(err)
	}
	if err := FsckConnectivity(dir); err == nil {
		t.Fatal("a repository missing a blob passed")
	}
}
