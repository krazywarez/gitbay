package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A trusted build's home is its repository's, kept between builds so
// tool caches survive: the same repository gets the same directory back,
// another repository a different one (#184).
func TestTrustedHomeIsPerRepositoryAndKept(t *testing.T) {
	work := t.TempDir()
	a, done, err := buildHome(work, job{ID: 1, Repo: "alice/app", Trusted: true})
	if err != nil {
		t.Fatal(err)
	}
	done()
	if _, err := os.Stat(a); err != nil {
		t.Fatalf("trusted home removed after its build: %v", err)
	}
	b, done, err := buildHome(work, job{ID: 2, Repo: "bob/app", Trusted: true})
	if err != nil {
		t.Fatal(err)
	}
	done()
	if a == b {
		t.Fatalf("two repositories share a build home: %s", a)
	}
	again, done, _ := buildHome(work, job{ID: 3, Repo: "alice/app", Trusted: true})
	done()
	if again != a {
		t.Fatalf("build home moved between builds: %s then %s", a, again)
	}
	for _, dir := range []string{a, b} {
		rel, err := filepath.Rel(filepath.Join(work, "trusted-home"), dir)
		if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
			t.Fatalf("build home %s is not under %s/trusted-home", dir, work)
		}
		st, err := os.Stat(dir)
		if err != nil {
			t.Fatal(err)
		}
		if st.Mode().Perm() != 0o700 {
			t.Fatalf("build home mode %o, want 0700", st.Mode().Perm())
		}
	}
}

// An untrusted build gets a home of its own, outside the trusted root,
// removed when the build ends: nothing a fork's build writes reaches a
// later build of the repository (#255).
func TestUntrustedHomeIsDisposable(t *testing.T) {
	work := t.TempDir()
	trusted, done, err := buildHome(work, job{ID: 1, Repo: "alice/app", Trusted: true})
	if err != nil {
		t.Fatal(err)
	}
	done()
	home, done, err := buildHome(work, job{ID: 2, Repo: "alice/app"})
	if err != nil {
		t.Fatal(err)
	}
	if home == trusted || strings.HasPrefix(home, filepath.Join(work, "trusted-home")) {
		t.Fatalf("untrusted build got a trusted home: %s", home)
	}
	// What the Go module cache leaves behind: read-only directories.
	cache := filepath.Join(home, "go", "pkg", "mod", "example.com", "m@v1")
	if err := os.MkdirAll(cache, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cache, "go.mod"), []byte("module m\n"), 0o444); err != nil {
		t.Fatal(err)
	}
	os.Chmod(cache, 0o555)
	os.Chmod(filepath.Dir(cache), 0o555)
	done()
	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Fatalf("untrusted home left behind: %v", err)
	}
}

// A repository path is server-validated, but a trusted home must still
// never resolve outside the runner's home root.
func TestBuildHomeRefusesTraversal(t *testing.T) {
	if _, _, err := buildHome(t.TempDir(), job{Repo: "../../etc", Trusted: true}); err == nil {
		t.Fatal("a traversing repository path produced a build home")
	}
}
