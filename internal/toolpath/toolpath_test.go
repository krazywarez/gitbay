package toolpath

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestLookResolvesToAnAbsolutePath(t *testing.T) {
	got := Look("go")
	if !filepath.IsAbs(got) {
		t.Fatalf("Look(go) = %q, want an absolute path", got)
	}
	want, err := exec.LookPath("go")
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Errorf("Look(go) = %q, want %q", got, want)
	}
}

// A second call must not search again: resolving once is the point, and a
// daemon that re-resolved would follow a PATH that changed under it.
func TestLookIsResolvedOnce(t *testing.T) {
	first := Look("go")
	if second := Look("go"); second != first {
		t.Errorf("Look(go) returned %q then %q", first, second)
	}
}

// An absent tool falls back to the bare name so exec reports it the way
// it always did, and Verify names it.
func TestMissingToolFallsBackAndIsReported(t *testing.T) {
	const name = "gitbay-no-such-tool-exists"
	if got := Look(name); got != name {
		t.Errorf("Look(%q) = %q, want the bare name back", name, got)
	}
	err := Verify()
	if err == nil {
		t.Fatal("Verify found nothing missing after an unresolvable lookup")
	}
	if !strings.Contains(err.Error(), name) {
		t.Errorf("Verify error %q does not name the missing tool", err)
	}
}
