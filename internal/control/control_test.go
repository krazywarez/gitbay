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

// TestBuildJobsIsAReadCommand pins the properties the surfaces depend on:
// the web build page and a read-scoped API token both need it over GET.
func TestBuildJobsIsAReadCommand(t *testing.T) {
	cmd, _, ok := Lookup([]string{"build", "jobs"})
	if !ok {
		t.Fatal("build jobs not registered")
	}
	if !cmd.ReadOnly {
		t.Error("build jobs must be ReadOnly; listing jobs changes nothing")
	}
	if cmd.SSHOnly {
		t.Error("build jobs must not be SSHOnly; the web and the app need it")
	}
}

// A merge request's dedup key must not collide with a commit sha. It did:
// a bare "#N" in a commit message recorded (issue, sha) first, and the
// description's "Closes #N" then found the key taken and silently gave
// up. That is how krz/gitbay-ios#8 stayed open after its own MR merged.
func TestMRDedupKeyCannotCollideWithASHA(t *testing.T) {
	key := mrRefKey(24)
	if key == "51b6a14eab49ab08e890597653fcf02f8f38f3d6" || len(key) == 40 {
		t.Errorf("mrRefKey(24) = %q, which is shaped like a sha", key)
	}
	if key != "mr-24" {
		t.Errorf("mrRefKey(24) = %q, want \"mr-24\"", key)
	}
	if mrRefKey(24) == mrRefKey(25) {
		t.Error("different merge requests share a dedup key")
	}
}
