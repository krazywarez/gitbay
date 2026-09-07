package main

import (
	"os"
	"path/filepath"
	"testing"
)

// The build home is per repository: one shared home let a step poison
// the module cache or plant a .gitconfig that another repository's build
// would honour (#184).
func TestBuildHomeIsPerRepository(t *testing.T) {
	work := t.TempDir()
	a, err := buildHomeFor(work, "alice/app")
	if err != nil {
		t.Fatal(err)
	}
	b, err := buildHomeFor(work, "bob/app")
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatalf("two repositories share a build home: %s", a)
	}
	for _, dir := range []string{a, b} {
		rel, err := filepath.Rel(filepath.Join(work, "home"), dir)
		if err != nil || rel == "." || filepath.IsAbs(rel) || rel[0] == '.' {
			t.Fatalf("build home %s is not under %s/home", dir, work)
		}
		st, err := os.Stat(dir)
		if err != nil {
			t.Fatalf("build home not created: %v", err)
		}
		if st.Mode().Perm() != 0o700 {
			t.Fatalf("build home mode %o, want 0700", st.Mode().Perm())
		}
	}
	// The same repository gets the same home back: that is what makes it
	// a cache.
	again, _ := buildHomeFor(work, "alice/app")
	if again != a {
		t.Fatalf("build home moved between builds: %s then %s", a, again)
	}
}

// A repository path is server-validated, but the home must still never
// resolve outside the runner's home root.
func TestBuildHomeRefusesTraversal(t *testing.T) {
	work := t.TempDir()
	if _, err := buildHomeFor(work, "../../etc"); err == nil {
		t.Fatal("a traversing repository path produced a build home")
	}
}
