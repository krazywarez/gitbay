package main

import (
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"gitbay.org/gitbay/internal/control"
	"gitbay.org/gitbay/internal/web"
)

// quotedRe finds the shapes a command appears in on a page: inline in
// <code>, one per line in a <pre class="quickstart"> quickstart block, or
// one per line in a <pre class="message"> block (settings/login pages quote
// commands there without wrapping each one in <code>). All need (?s) so a
// multi-line <pre> is captured as one match.
var quotedRe = regexp.MustCompile(`(?s)<code>(.*?)</code>|<pre class="quickstart"[^>]*>(.*?)</pre>|<pre class="message"[^>]*>(.*?)</pre>`)

// commandArgv reads the literal words at the front of a quoted command
// line — the part naming the command rather than its arguments — and
// stops at the first flag, template action, literal ellipsis, or
// placeholder (a bare `<name>` or its HTML-escaped form `&lt;name&gt;`),
// since those mark the boundary between "what command" and "what
// argument".
func commandArgv(rest string) []string {
	var argv []string
	for _, tok := range strings.Fields(rest) {
		if strings.HasPrefix(tok, "-") || strings.Contains(tok, "{{") || strings.Contains(tok, "...") ||
			strings.HasPrefix(tok, "<") || strings.HasPrefix(tok, "&lt;") {
			break
		}
		argv = append(argv, tok)
	}
	return argv
}

// checkGitbayCommand resolves argv (the words after "gitbay ") against the
// CLI's own cobra tree, the way a person would type it. cobra's Find walks
// one token at a time and, on the first token that names no child, simply
// returns the last command it did match with err == nil and the
// unmatched tokens as leftovers — so `admin user creat` resolves to the
// "user" group instead of failing, unless the caller checks for leftover
// args itself. A leaf command (no subcommands of its own) legitimately
// takes further words as positional arguments (a repository name, for
// instance), so leftover args on a leaf are not an error — only leftover
// args on a command that still has subcommands are, since that means the
// next word failed to name one of them.
func checkGitbayCommand(root *cobra.Command, argv []string) error {
	found, rest, err := root.Find(argv)
	if err != nil {
		return err
	}
	if found == root {
		return fmt.Errorf("does not resolve")
	}
	if found.HasSubCommands() && len(rest) > 0 {
		return fmt.Errorf("%q is not a subcommand of %q", rest[0], found.CommandPath())
	}
	return nil
}

// checkSSHCommand resolves argv against the control registry the way
// "ssh git@host ..." dispatches it. control.Lookup matches the longest
// registered path that is a prefix of argv and reports ok=true even when
// trailing words remain unconsumed — e.g. argv ["whoami", "bogus"]
// matches the registered "whoami" leaf and silently drops "bogus".
// Every quoted ssh command reaching this function has already had its
// flags, template actions, ellipses and placeholders trimmed by
// commandArgv, so nothing legitimate is left dangling after a real
// command's words: any remaining word is either a typo'd attempt at a
// deeper command (checked against the registry below, for a precise
// message) or bare stray text, and both are bugs in the quoted line.
func checkSSHCommand(argv []string) error {
	found, rest, ok := control.Lookup(argv)
	if !ok {
		return fmt.Errorf("%s is not in the control registry", strings.Join(argv, " "))
	}
	if len(rest) == 0 {
		return nil
	}
	next := rest[0]
	for _, cmd := range control.Commands() {
		if len(cmd.Path) > len(found.Path) && cmd.Path[len(found.Path)] == next &&
			strings.Join(cmd.Path[:len(found.Path)], " ") == strings.Join(found.Path, " ") {
			return fmt.Errorf("%q is not a word %s takes further — did you mean %s?", next, strings.Join(found.Path, " "), strings.Join(cmd.Path, " "))
		}
	}
	return fmt.Errorf("%q is not consumed by %s", next, strings.Join(found.Path, " "))
}

// TestTemplateQuotedCommandsResolve runs every command quoted in a web
// template through the same registry the server uses, so a renamed
// command fails CI instead of shipping a dead instruction (#263).
//
// A line starting "gitbay " is checked against the CLI's own command
// tree with cobra's Find, since the CLI's grouping words (like "auth")
// are not part of the server's argv. A line starting "ssh git@{{.Host}}
// " is checked directly against control.Lookup, since that is exactly
// the argv the server receives.
func TestTemplateQuotedCommandsResolve(t *testing.T) {
	root := newRoot()
	for _, name := range web.Pages() {
		src, err := web.TemplateSource(name)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		for _, m := range quotedRe.FindAllStringSubmatch(src, -1) {
			block := m[1] + m[2] + m[3]
			for _, line := range strings.Split(block, "\n") {
				if i := strings.Index(line, "#"); i >= 0 {
					line = line[:i]
				}
				line = strings.TrimSpace(line)
				switch {
				case strings.HasPrefix(line, "gitbay "):
					argv := commandArgv(strings.TrimPrefix(line, "gitbay "))
					if len(argv) == 0 {
						continue
					}
					if err := checkGitbayCommand(root, argv); err != nil {
						t.Errorf("%s: %q: gitbay %s: %v", name, line, strings.Join(argv, " "), err)
					}
				case strings.HasPrefix(line, "ssh git@{{.Host}} "):
					argv := commandArgv(strings.TrimPrefix(line, "ssh git@{{.Host}} "))
					if len(argv) == 0 {
						continue
					}
					if err := checkSSHCommand(argv); err != nil {
						t.Errorf("%s: %q: %v", name, line, err)
					}
				}
			}
		}
	}
}

func TestCommandArgv(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"org delete {{$org}} --yes", []string{"org", "delete"}},
		{"admin user create &lt;name&gt; --key - &lt; key.pub", []string{"admin", "user", "create"}},
		{"admin ...", []string{"admin"}},
		{"whoami", []string{"whoami"}},
		{"web sessions list", []string{"web", "sessions", "list"}},
		{"snippet create <file> < file", []string{"snippet", "create"}},
	}
	for _, c := range cases {
		got := commandArgv(c.in)
		if strings.Join(got, " ") != strings.Join(c.want, " ") {
			t.Errorf("commandArgv(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestCheckGitbayCommand(t *testing.T) {
	root := newRoot()
	cases := []struct {
		name    string
		argv    []string
		wantErr bool
	}{
		{"real leaf", []string{"auth", "export"}, false},
		{"real leaf two groups deep", []string{"admin", "user", "create"}, false},
		{"leaf with a positional leftover is fine", []string{"org", "delete", "krz/gitbay"}, false},
		{"typo'd subcommand under a group", []string{"admin", "user", "creat"}, true},
		{"top-level typo", []string{"bogus"}, true},
		{"old wrong top-level path", []string{"account", "export"}, true},
	}
	for _, c := range cases {
		err := checkGitbayCommand(root, c.argv)
		if (err != nil) != c.wantErr {
			t.Errorf("%s: checkGitbayCommand(%v) error = %v, wantErr %v", c.name, c.argv, err, c.wantErr)
		}
	}
}

func TestCheckSSHCommand(t *testing.T) {
	cases := []struct {
		name    string
		argv    []string
		wantErr bool
	}{
		{"real leaf", []string{"whoami"}, false},
		{"real leaf three deep", []string{"web", "sessions", "list"}, false},
		{"stray trailing word", []string{"whoami", "bogus"}, true},
		{"unregistered path entirely", []string{"auth", "whoami"}, true},
		{"typo'd word past a real prefix", []string{"web", "sessions", "listing"}, true},
	}
	for _, c := range cases {
		err := checkSSHCommand(c.argv)
		if (err != nil) != c.wantErr {
			t.Errorf("%s: checkSSHCommand(%v) error = %v, wantErr %v", c.name, c.argv, err, c.wantErr)
		}
	}
}
