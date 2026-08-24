package control

import (
	"strings"
	"testing"

	"gitbay.org/gitbay/internal/protocol"
)

// TestEveryCommandReachableFromBareSSH asserts that each registered command's
// path, rendered exactly as a user would type it after `ssh <host>`, resolves
// back to that command through the tokenizer and Lookup. This is the guard
// that keeps the forge CLI optional.
func TestEveryCommandReachableFromBareSSH(t *testing.T) {
	cmds := Commands()
	if len(cmds) == 0 {
		t.Fatal("no commands registered")
	}
	for _, cmd := range cmds {
		line := strings.Join(cmd.Path, " ")
		argv, err := protocol.Tokenize(line)
		if err != nil {
			t.Errorf("command %q not tokenizable: %v", line, err)
			continue
		}
		got, rest, ok := Lookup(argv)
		if !ok {
			t.Errorf("command %q not found by Lookup", line)
			continue
		}
		if strings.Join(got.Path, " ") != line || len(rest) != 0 {
			t.Errorf("Lookup(%q) resolved to %q with rest %v", line, strings.Join(got.Path, " "), rest)
		}
		if cmd.Run == nil {
			t.Errorf("command %q has no Run", line)
		}
		if cmd.Summary == "" {
			t.Errorf("command %q has no summary", line)
		}
	}
}

func TestLookupLongestMatch(t *testing.T) {
	// "keys list" must not resolve to a hypothetical shorter prefix and
	// unknown commands must not match.
	if _, _, ok := Lookup([]string{"keys"}); ok {
		t.Error("bare \"keys\" resolved; group prefixes must not be runnable")
	}
	if _, _, ok := Lookup([]string{"nope"}); ok {
		t.Error("unknown command resolved")
	}
	cmd, rest, ok := Lookup([]string{"keys", "list", "--json"})
	if !ok || strings.Join(cmd.Path, " ") != "keys list" || len(rest) != 1 {
		t.Errorf("Lookup keys list --json = %v %v %v", cmd.Path, rest, ok)
	}
}
