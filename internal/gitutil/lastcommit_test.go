package gitutil

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@e",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@e")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func write(t *testing.T, dir, rel, body string) {
	t.Helper()
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLastCommits(t *testing.T) {
	dir := t.TempDir()
	git(t, dir, "init", "-q", "-b", "main")

	write(t, dir, "README.md", "one\n")
	write(t, dir, "src/main.go", "one\n")
	write(t, dir, "docs/guide.md", "one\n")
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-qm", "first")

	// Touching a file deep inside src/ must move the src/ entry without
	// disturbing README.md or docs/.
	write(t, dir, "src/lib/helper.go", "two\n")
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-qm", "add helper")

	write(t, dir, "README.md", "three\n")
	git(t, dir, "commit", "-qam", "update readme")

	got := LastCommits(dir, "main", "", []string{"README.md", "src", "docs"})
	for name, wantSubject := range map[string]string{
		"README.md": "update readme",
		"src":       "add helper",
		"docs":      "first",
	} {
		c, ok := got[name]
		if !ok {
			t.Errorf("%s unresolved", name)
			continue
		}
		if c.Subject != wantSubject {
			t.Errorf("%s = %q, want %q", name, c.Subject, wantSubject)
		}
		if c.SHA == "" || c.When.IsZero() {
			t.Errorf("%s missing sha or time: %+v", name, c)
		}
	}

	// Inside a subdirectory the prefix is stripped, so entries resolve
	// against their own names rather than full paths.
	sub := LastCommits(dir, "main", "src", []string{"main.go", "lib"})
	if c := sub["main.go"]; c.Subject != "first" {
		t.Errorf("src/main.go = %q, want %q", c.Subject, "first")
	}
	if c := sub["lib"]; c.Subject != "add helper" {
		t.Errorf("src/lib = %q, want %q", c.Subject, "add helper")
	}

	// A name nobody asked about never appears, and an unknown one is
	// simply absent rather than an error.
	if _, ok := sub["README.md"]; ok {
		t.Error("src listing leaked a root entry")
	}
	if _, ok := LastCommits(dir, "main", "", []string{"nope"})["nope"]; ok {
		t.Error("resolved a path that does not exist")
	}
	if LastCommits(dir, "main", "", nil) != nil {
		t.Error("empty name list should not run git")
	}
}
