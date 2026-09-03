package control

import (
	"bytes"
	"encoding/json"
	"io"
	"slices"
	"strings"
	"testing"

	"gitbay.org/gitbay/internal/protocol"
	"gitbay.org/gitbay/internal/store"
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

// TestEveryCommandDocumentsItsUsage is what makes `help <prefix>` and
// `gitbay <cmd> --help` worth typing: both render Usage, so a command that
// omits it documents nothing. Usage opens with the command path so the
// printed line can be typed as-is, and the summary must not carry the
// argument syntax it used to.
func TestEveryCommandDocumentsItsUsage(t *testing.T) {
	for _, cmd := range Commands() {
		path := strings.Join(cmd.Path, " ")
		if cmd.Usage == "" {
			t.Errorf("command %q has no Usage", path)
			continue
		}
		if cmd.Usage != path && !strings.HasPrefix(cmd.Usage, path+" ") {
			t.Errorf("command %q has Usage %q, which does not open with the command path", path, cmd.Usage)
		}
		if strings.Contains(cmd.Summary, ": "+path) {
			t.Errorf("command %q still carries its usage in the summary: %q", path, cmd.Summary)
		}
	}
}

// TestHelpPrefixNarrowsAndShowsFlags covers the reason the command exists:
// before this, reading one command's flags meant reading all of them.
func TestHelpPrefixNarrowsAndShowsFlags(t *testing.T) {
	var buf bytes.Buffer
	c := &Ctx{Stdout: &buf, Stderr: io.Discard}
	if code := runHelp(c, []string{"issue"}); code != protocol.ExitOK {
		t.Fatalf("help issue exited %d", code)
	}
	out := buf.String()
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if strings.HasPrefix(line, "  ") {
			continue // the indented usage line
		}
		if !strings.HasPrefix(line, "issue ") {
			t.Errorf("help issue listed an unrelated command: %q", line)
		}
	}
	if !strings.Contains(out, "--state open|closed|all") {
		t.Error("help issue did not print issue list's flags")
	}
}

func TestHelpUnknownPrefixIsNotFound(t *testing.T) {
	var buf bytes.Buffer
	c := &Ctx{Stdout: &buf, Stderr: io.Discard}
	if code := runHelp(c, []string{"nope"}); code != protocol.ExitNotFound {
		t.Errorf("help nope exited %d, want %d", code, protocol.ExitNotFound)
	}
}

// TestHelpListsEveryCommandSorted pins the unfiltered listing: one row per
// registered command, ordered so a noun's commands sit together.
func TestHelpListsEveryCommandSorted(t *testing.T) {
	var buf bytes.Buffer
	c := &Ctx{Stdout: &buf, Stderr: io.Discard, JSON: true}
	if code := runHelp(c, nil); code != protocol.ExitOK {
		t.Fatalf("help exited %d", code)
	}
	var env struct {
		Data []helpEntry `json:"data"`
	}
	if err := json.Unmarshal(buf.Bytes(), &env); err != nil {
		t.Fatalf("help --json: %v", err)
	}
	if len(env.Data) != len(Commands()) {
		t.Errorf("help listed %d commands, registry has %d", len(env.Data), len(Commands()))
	}
	if !slices.IsSortedFunc(env.Data, func(a, b helpEntry) int { return strings.Compare(a.Path, b.Path) }) {
		t.Error("help output is not sorted by path")
	}
}

// TestStdinCommandsReadStdin: a command whose usage says its input arrives
// on stdin must set ReadsStdin, or Dispatch hands it an empty reader and
// --file - silently stores nothing (#127).
func TestStdinCommandsReadStdin(t *testing.T) {
	for _, cmd := range Commands() {
		u := cmd.Usage
		wants := strings.Contains(u, "--file -") || strings.Contains(u, "< ") ||
			strings.Contains(u, "stdin") || strings.Contains(u, "--key -")
		if wants && !cmd.ReadsStdin {
			t.Errorf("%s: usage %q reads stdin but ReadsStdin is not set", strings.Join(cmd.Path, " "), u)
		}
	}
}

// TestAdminNounGatedInDispatch: every admin command is refused for a
// non-admin by the dispatcher itself, before any handler runs.
func TestAdminNounGatedInDispatch(t *testing.T) {
	for _, cmd := range Commands() {
		if cmd.Path[0] != "admin" {
			continue
		}
		var out, errOut bytes.Buffer
		c := &Ctx{User: store.User{Username: "nobody"}, Scope: "full", Stdout: &out, Stderr: &errOut}
		if code := Dispatch(c, cmd.Path); code != protocol.ExitDenied {
			t.Errorf("%s: non-admin got exit %d, want %d", strings.Join(cmd.Path, " "), code, protocol.ExitDenied)
		}
	}
}
