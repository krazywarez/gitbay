package gitutil

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestDiffNumstat(t *testing.T) {
	dir := t.TempDir()
	git(t, dir, "init", "-q", "-b", "main")
	write(t, dir, "a.txt", "one\ntwo\nthree\n")
	write(t, dir, "old.txt", strings.Repeat("line\n", 20))
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-qm", "base")
	git(t, dir, "tag", "base")

	write(t, dir, "a.txt", "one\n2\n3\nthree\n")
	write(t, dir, "new.txt", "hello\n")
	if err := os.WriteFile(filepath.Join(dir, "bin.dat"), []byte{0, 1, 2, 0, 3}, 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "mv", "old.txt", "moved.txt")
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-qm", "head")

	got, err := DiffNumstat(dir, "base", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	want := []NumStat{
		{Path: "a.txt", Added: 2, Deleted: 1, Status: "M"},
		{Path: "bin.dat", Added: -1, Deleted: -1, Status: "A"},
		{Path: "moved.txt", Added: 0, Deleted: 0, Status: "R"},
		{Path: "new.txt", Added: 1, Deleted: 0, Status: "A"},
	}
	slices.SortFunc(got, func(a, b NumStat) int { return strings.Compare(a.Path, b.Path) })
	if !slices.Equal(got, want) {
		t.Errorf("got %+v\nwant %+v", got, want)
	}
}
