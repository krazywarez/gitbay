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
	gitDir := filepath.Join(dir, ".git")
	if err := FsckConnectivity(gitDir); err != nil {
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
	if err := FsckConnectivity(gitDir); err == nil {
		t.Fatal("a repository missing a blob passed")
	}
}

// A directory that is not a repository fails, even inside one: git does
// not search upward and check the enclosing repository instead.
func TestFsckConnectivityRefusesANonRepository(t *testing.T) {
	dir := t.TempDir()
	git(t, dir, "init", "-q", "-b", "main")
	write(t, dir, "a.txt", "a\n")
	git(t, dir, "add", "a.txt")
	git(t, dir, "commit", "-q", "-m", "one")
	inner := filepath.Join(dir, "repos", "x.git")
	if err := os.MkdirAll(inner, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := FsckConnectivity(inner); err == nil {
		t.Fatal("an empty directory inside a repository passed")
	}
}
