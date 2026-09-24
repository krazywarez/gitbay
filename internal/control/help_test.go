package control

import (
	"regexp"
	"slices"
	"strings"
	"testing"

	"gitbay.org/gitbay/internal/protocol"
)

var usageFlag = regexp.MustCompile(`--[a-z][a-z0-9-]*`)

// trailingRedirect strips a shell redirect an example ends with, the way a
// person's shell would before gitbay ever sees argv: protocol.Tokenize
// rejects a bare < or > as a shell metacharacter, so the redirected part
// is not something the command's own tokenizer parses.
var trailingRedirect = regexp.MustCompile(`^(.*?)\s+[<>]\s*\S+$`)

// Help is written once, in the registry. Every flag in a usage line has
// a description, every description names a flag in the usage line, and
// every command has an example that runs it.
func TestHelpIsComplete(t *testing.T) {
	for _, cmd := range Commands() {
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
			toParse := ex
			if m := trailingRedirect.FindStringSubmatch(ex); m != nil {
				toParse = m[1]
			}
			argv, err := protocol.Tokenize(toParse)
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
