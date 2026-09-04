package control

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// recordEventKind pulls the kind out of a RecordEvent call. The kind is
// either a literal or a literal prefix concatenated with a variable, and
// the second shape is why this reads source rather than trusting a list.
var recordEventKind = regexp.MustCompile(`RecordEvent\([^,]+,\s*[^,]+,\s*"([a-z.]+)"`)

// TestEventKindsAreRecorded keeps the published list and the code
// together: an event added without documenting it, or documented without
// being emitted, fails here. Webhook subscribers filter on these strings,
// so a name that exists in only one of the two places is a subscription
// that silently never fires (#112).
func TestEventKindsAreRecorded(t *testing.T) {
	emitted := map[string]bool{}
	roots := []string{".", filepath.Join("..", "hookd")}
	for _, root := range roots {
		entries, err := os.ReadDir(root)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			if !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
				continue
			}
			src, err := os.ReadFile(filepath.Join(root, e.Name()))
			if err != nil {
				t.Fatal(err)
			}
			for _, m := range recordEventKind.FindAllStringSubmatch(string(src), -1) {
				emitted[m[1]] = true
			}
		}
	}
	// The kinds built from a variable suffix, which the pattern above
	// sees only as their prefix. Each is spelled out so the list stays
	// exhaustive rather than approximate.
	for _, k := range []string{
		"build.success", "build.failure", // "build."+args[1]
		"issue.closed", "issue.open", // "issue."+state
		"repo.archived", "repo.unarchived", // "repo."+verb+"d"
		"issue.commented", "mr.commented", // thread.event
		"issue.milestoned", "mr.milestoned", // noun+".milestoned"
	} {
		emitted[k] = true
	}
	// Prefixes the pattern caught from those concatenations.
	for _, partial := range []string{"build.", "issue.", "repo."} {
		delete(emitted, partial)
	}

	for _, k := range EventKinds {
		if !emitted[k] {
			t.Errorf("EventKinds lists %q but nothing records it", k)
		}
	}
	for k := range emitted {
		if !slices.Contains(EventKinds, k) {
			t.Errorf("%q is recorded but missing from EventKinds; add it, and to the API wiki page", k)
		}
	}
}
