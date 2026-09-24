package control

import (
	"regexp"
	"slices"
	"strings"
	"testing"

	"gitbay.org/gitbay/internal/protocol"
)

var usageFlag = regexp.MustCompile(`--[a-z][a-z0-9-]*`)

// helpCovered restricts TestHelpIsComplete to the path prefixes whose help
// text this commit filled in. Task 4.3 fills the rest and removes this set.
// "org label" and "org milestone" name the two prefixes under "org" this
// commit covers; the rest of "org" (teams, members, profile) is not.
// widened to every command in the next commit
var helpCovered = []string{
	"issue", "mr", "build", "release", "milestone", "label", "search",
	"dashboard", "feed", "status", "wiki", "snippet", "repo",
	"org label", "org milestone",
}

func isHelpCovered(path []string) bool {
	joined := strings.Join(path, " ")
	for _, prefix := range helpCovered {
		if joined == prefix || strings.HasPrefix(joined, prefix+" ") {
			return true
		}
	}
	return false
}

// Help is written once, in the registry. Every flag in a usage line has
// a description, every description names a flag in the usage line, and
// every command has an example that runs it.
func TestHelpIsComplete(t *testing.T) {
	for _, cmd := range Commands() {
		if !isHelpCovered(cmd.Path) {
			continue
		}
		path := strings.Join(cmd.Path, " ")
		inUsage := map[string]bool{}
		for _, f := range usageFlag.FindAllString(cmd.Usage, -1) {
			if f != "--json" {
				inUsage[f] = true
			}
		}
		described := map[string]bool{}
		for _, f := range cmd.Flags {
			described[f.Name] = true
			if f.Desc == "" {
				t.Errorf("%s: %s has no description", path, f.Name)
			}
			if !inUsage[f.Name] {
				t.Errorf("%s: %s is described but not in the usage", path, f.Name)
			}
		}
		for f := range inUsage {
			if !described[f] {
				t.Errorf("%s: %s is in the usage with no description", path, f)
			}
		}
		if len(cmd.Examples) == 0 {
			t.Errorf("%s: no example", path)
		}
		for _, ex := range cmd.Examples {
			argv, err := protocol.Tokenize(ex)
			if err != nil {
				t.Errorf("%s: example %q: %v", path, ex, err)
				continue
			}
			got, _, ok := Lookup(argv)
			if !ok || !slices.Equal(got.Path, cmd.Path) {
				t.Errorf("%s: example %q runs %v", path, ex, got.Path)
			}
		}
	}
}
