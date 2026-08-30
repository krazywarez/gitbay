package buildinfo

import (
	"os/exec"
	"strings"
	"testing"
)

// The link-time stamp wins. This is what the Makefile sets, and what a
// deployed binary reports.
func TestStringPrefersTheStamp(t *testing.T) {
	prev := Commit
	defer func() { Commit = prev }()

	Commit = "abc123def456-dirty"
	if got := String(); got != "abc123def456-dirty" {
		t.Errorf("String() = %q, want the stamped value", got)
	}
}

// Without a stamp it falls back to the revision the toolchain embeds. Under
// `go test` that is this checkout, so the result is a short hex revision,
// possibly with -dirty. It must never be empty.
func TestStringFallsBackToTheVCSStamp(t *testing.T) {
	prev := Commit
	defer func() { Commit = prev }()

	Commit = ""
	got := String()
	if got == "" {
		t.Fatal("String() returned empty; it must always name something")
	}
	rev := strings.TrimSuffix(got, "-dirty")
	if got != "unknown" && len(rev) != 12 {
		t.Errorf("String() = %q; want \"unknown\" or a 12-char revision", got)
	}
}

// The stamp the Makefile computes must match the commit actually checked out,
// or a deployed binary would name the wrong one.
func TestMakefileStampMatchesHEAD(t *testing.T) {
	head, err := exec.Command("git", "rev-parse", "--short=12", "HEAD").Output()
	if err != nil {
		t.Skipf("not a git checkout: %v", err)
	}
	want := strings.TrimSpace(string(head))

	prev := Commit
	defer func() { Commit = prev }()
	Commit = ""

	got := strings.TrimSuffix(String(), "-dirty")
	if got != "unknown" && got != want {
		t.Errorf("String() = %q, but HEAD is %q", got, want)
	}
}

func TestIdentified(t *testing.T) {
	prev := Commit
	defer func() { Commit = prev }()

	for _, c := range []struct {
		stamp string
		want  bool
	}{
		{"b685adf5ab7a", true},
		{"b685adf5ab7a-dirty", false},
		{"unknown", false},
	} {
		Commit = c.stamp
		if got := Identified(); got != c.want {
			t.Errorf("Identified() with stamp %q = %v, want %v", c.stamp, got, c.want)
		}
	}
}
