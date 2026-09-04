package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The default must not be a fixed name inside a world-writable directory:
// another local user could create it first, and MkdirAll would accept
// theirs. This is the process that clones repositories and exports build
// secrets into step environments (go:S5445, #153).
func TestDefaultWorkdirIsNotInSharedTmp(t *testing.T) {
	got := defaultWorkdir()
	cache, err := os.UserCacheDir()
	if err != nil || cache == "" {
		t.Skip("no user cache directory on this machine; the tmp fallback is the tested path")
	}
	if !strings.HasPrefix(got, cache) {
		t.Errorf("default workdir %q is not under the user cache %q", got, cache)
	}
	if strings.HasPrefix(got, os.TempDir()+string(filepath.Separator)) {
		t.Errorf("default workdir %q is still inside the shared temp directory", got)
	}
}

func TestCheckWorkdirAcceptsAPrivateDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "work")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := checkWorkdir(dir); err != nil {
		t.Fatalf("a private directory this user owns was refused: %v", err)
	}
}

// A workspace we own that is merely too permissive is tightened, not
// refused: every runner before this one made its workspace 0755, and
// refusing would take the runner down on upgrade over a permission it is
// entitled to change.
func TestCheckWorkdirTightensOurOwnDirectory(t *testing.T) {
	base := t.TempDir()
	for _, mode := range []os.FileMode{0o755, 0o770, 0o777} {
		dir := filepath.Join(base, fmt.Sprintf("mode-%o", mode))
		if err := os.MkdirAll(dir, mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(dir, mode); err != nil { // MkdirAll applies umask
			t.Fatal(err)
		}
		if err := checkWorkdir(dir); err != nil {
			t.Fatalf("mode %04o: refused a directory we own: %v", mode, err)
		}
		fi, err := os.Stat(dir)
		if err != nil {
			t.Fatal(err)
		}
		if got := fi.Mode().Perm(); got != 0o700 {
			t.Errorf("mode %04o was left at %04o, want 0700", mode, got)
		}
	}
}

// What cannot be repaired is refused: a symlink is not ours to correct,
// and it is what an attacker leaves behind.
func TestCheckWorkdirRefusesUnsafeDirectories(t *testing.T) {
	base := t.TempDir()

	target := filepath.Join(base, "elsewhere")
	if err := os.MkdirAll(target, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := checkWorkdir(link); err == nil {
		t.Error("a symlinked workdir was accepted")
	} else if !strings.Contains(err.Error(), "symlink") {
		t.Errorf("symlink refusal does not say why: %v", err)
	}
}

func TestCheckWorkdirRefusesAFile(t *testing.T) {
	f := filepath.Join(t.TempDir(), "notadir")
	if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := checkWorkdir(f); err == nil {
		t.Error("a regular file was accepted as a workdir")
	}
}
