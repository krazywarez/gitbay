package gitutil

import (
	"os/exec"
	"strings"
	"testing"
)

func TestSubjects(t *testing.T) {
	dir := t.TempDir()
	git(t, dir, "init", "-q", "-b", "main")

	write(t, dir, "a", "one\n")
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-qm", "first thing")
	first := rev(t, dir, "HEAD")

	write(t, dir, "a", "two\n")
	git(t, dir, "commit", "-qam", "second thing")
	second := rev(t, dir, "HEAD")

	got := Subjects(dir, []string{second, first})
	if got[first] != "first thing" || got[second] != "second thing" {
		t.Fatalf("Subjects = %v", got)
	}

	// A build outlives the commit it ran on when a branch is
	// force-pushed. The shas that do resolve still come back.
	gone := strings.Repeat("1", 40)
	got = Subjects(dir, []string{gone, first})
	if got[first] != "first thing" {
		t.Errorf("a missing sha lost the rest: %v", got)
	}
	if _, ok := got[gone]; ok {
		t.Errorf("resolved a sha that is not there: %v", got)
	}

	if Subjects(dir, nil) != nil {
		t.Error("empty sha list should not run git")
	}
}

func rev(t *testing.T, dir, ref string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", dir, "rev-parse", ref).Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(out))
}
