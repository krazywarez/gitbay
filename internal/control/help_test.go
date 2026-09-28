package control

import (
	"bytes"
	"regexp"
	"slices"
	"strings"
	"testing"

	"gitbay.org/gitbay/internal/protocol"
)

func helpOut(t *testing.T, term Term, prefix ...string) string {
	t.Helper()
	var out, errOut bytes.Buffer
	c := &Ctx{Stdout: &out, Stderr: &errOut, Term: term, Scope: "full"}
	c.Cfg.Server.SiteURL = "https://forge.test"
	if code := Dispatch(c, append([]string{"help"}, prefix...)); code != protocol.ExitOK {
		t.Fatalf("help %v: exit %d: %s", prefix, code, errOut.String())
	}
	return out.String()
}

func TestHelpVerb(t *testing.T) {
	out := helpOut(t, Term{Cols: 100}, "issue", "list")
	for _, want := range []string{
		"list issues\n",
		"USAGE\n  gitbay issue list [<owner/name>] [flags]\n",
		"FLAGS\n",
		"  --state open|closed|all",
		"which issues (default open)\n",
		"  --json",
		"EXAMPLES\n  gitbay issue list krz/gitbay --label bug --state all\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	plain := helpOut(t, Term{}, "issue", "list")
	if !strings.Contains(plain, "  ssh git@forge.test issue list krz/gitbay --label bug --state all\n") {
		t.Errorf("plain examples not ssh:\n%s", plain)
	}
	if !strings.Contains(plain, "USAGE\n  ssh git@forge.test issue list <owner/name> [flags]\n") {
		t.Errorf("plain usage:\n%s", plain)
	}
}

// TestHelpVerbUsageKeepsRequiredFlags covers a usage line with no
// optional flag to cut at: it prints whole, not truncated to "[flags]"
// as though --yes were optional.
func TestHelpVerbUsageKeepsRequiredFlags(t *testing.T) {
	out := helpOut(t, Term{Cols: 100}, "repo", "delete")
	if !strings.Contains(out, "USAGE\n  gitbay repo delete [<owner/name>] --yes\n") {
		t.Errorf("missing required --yes in usage:\n%s", out)
	}
	if strings.Contains(out, "[flags]") {
		t.Errorf("repo delete has no optional flags; should not print [flags]:\n%s", out)
	}
}

// TestHelpVerbUsageKeepsAlternative covers a usage line whose flag is
// one side of a "|" alternative, not an optional extra.
func TestHelpVerbUsageKeepsAlternative(t *testing.T) {
	out := helpOut(t, Term{Cols: 100}, "notifications", "read")
	if !strings.Contains(out, "USAGE\n  gitbay notifications read <id>... | --all\n") {
		t.Errorf("missing whole alternative in usage:\n%s", out)
	}
}

func TestHelpNoun(t *testing.T) {
	out := helpOut(t, Term{Cols: 100}, "issue")
	for _, want := range []string{"issues\n", "READ\n", "WRITE\n", "  list ", "  create ", "gitbay issue <verb> --help for flags.\n"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Index(out, "  list ") > strings.Index(out, "WRITE") {
		t.Errorf("list is not under READ:\n%s", out)
	}
}

func TestEveryNounHasASummary(t *testing.T) {
	for _, cmd := range Commands() {
		if nounSummaries[cmd.Path[0]] == "" {
			t.Errorf("no noun summary for %q", cmd.Path[0])
		}
	}
}

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

func TestCmdUsagePrefixesTheProgram(t *testing.T) {
	c := &Ctx{Cmd: Command{Path: []string{"keys", "remove"}, Usage: "keys remove <fingerprint>"}}
	c.Cfg.Server.SiteURL = "https://forge.test"
	if got := c.cmdUsage(); got != "ssh git@forge.test keys remove <fingerprint>" {
		t.Errorf("ssh form: %q", got)
	}
	c.Term = Term{Cols: 100}
	if got := c.cmdUsage(); got != "gitbay keys remove <fingerprint>" {
		t.Errorf("cli form, no CLIPath sent: %q", got)
	}
	c.CLIPath = "auth keys remove"
	if got := c.cmdUsage(); got != "gitbay auth keys remove <fingerprint>" {
		t.Errorf("cli form, mismatched registered path: %q", got)
	}
	c.Term = Term{}
	if got := c.cmdUsage(); got != "gitbay auth keys remove <fingerprint>" {
		t.Errorf("cli form off a terminal, mismatched registered path: %q", got)
	}

	c2 := &Ctx{Cmd: Command{Path: []string{"repo", "tree"}, Usage: "repo tree <owner/name> [<path>] [--ref <ref>]"}, Term: Term{Cols: 100}}
	if got := c2.cmdUsage(); got != "gitbay repo tree [<owner/name>] [<path>] [--ref <ref>]" {
		t.Errorf("optional owner/name: %q", got)
	}
	c2.CLIPath = "repo tree"
	if got := c2.cmdUsage(); got != "gitbay repo tree [<owner/name>] [<path>] [--ref <ref>]" {
		t.Errorf("matching CLIPath changes nothing: %q", got)
	}

	c3 := &Ctx{Cmd: Command{Path: []string{"repo", "topics", "add"}, Usage: "repo topics add <owner/name> <topic>..."}, Term: Term{Cols: 100}, CLIPath: "repo topics list"}
	if got := c3.cmdUsage(); got != "gitbay repo topics add [<owner/name>] <topic>..." {
		t.Errorf("CLIPath naming another command: %q", got)
	}
	c3.Cmd = Command{Path: []string{"repo", "topics"}, Usage: "repo topics <owner/name>"}
	if got := c3.cmdUsage(); got != "gitbay repo topics list [<owner/name>]" {
		t.Errorf("CLIPath extending the registered path: %q", got)
	}
	c3.CLIPath = " "
	if got := c3.cmdUsage(); got != "gitbay repo topics [<owner/name>]" {
		t.Errorf("blank CLIPath: %q", got)
	}
}

// TestShownBelow: another command listed beside the one the CLI named
// takes the CLI's parent when the CLI only regrouped the command (auth
// keys remove), and keeps its registered path when the CLI renamed the
// leaf (repo topics list for repo topics), since that says nothing about
// what the other command is called.
func TestShownBelow(t *testing.T) {
	cases := []struct {
		cliPath, registered, other, want string
	}{
		{"", "keys remove", "keys list", "keys list"},
		{"keys remove", "keys remove", "keys list", "keys list"},
		{"auth keys remove", "keys remove", "keys list", "auth keys list"},
		{"auth export", "account export", "account export extra", "auth export extra"},
		{"repo topics list", "repo topics", "repo topics add", "repo topics add"},
	}
	for _, tc := range cases {
		c := &Ctx{CLIPath: tc.cliPath}
		if got := c.shownBelow(tc.registered, tc.other); got != tc.want {
			t.Errorf("CLIPath %q, %q beside %q: got %q, want %q", tc.cliPath, tc.other, tc.registered, got, tc.want)
		}
	}
}

// TestHelpPrintsTheCLIPath: help reached through the CLI with a --path=
// prints the CLI's path in USAGE, the noun header and SEE ALSO.
func TestHelpPrintsTheCLIPath(t *testing.T) {
	via := func(t *testing.T, term Term, argv ...string) string {
		t.Helper()
		var out, errOut bytes.Buffer
		c := &Ctx{Stdout: &out, Stderr: &errOut, Term: term, Scope: "full"}
		c.Cfg.Server.SiteURL = "https://forge.test"
		if code := Dispatch(c, argv); code != protocol.ExitOK {
			t.Fatalf("%v: exit %d: %s", argv, code, errOut.String())
		}
		return out.String()
	}
	verb := via(t, Term{Cols: 100}, "--path=auth keys remove", "help", "keys", "remove")
	if !strings.Contains(verb, "USAGE\n  gitbay auth keys remove <fingerprint>") {
		t.Errorf("verb usage:\n%s", verb)
	}
	topics := via(t, Term{Cols: 100}, "--path=repo topics list", "help", "repo", "topics")
	for _, want := range []string{"USAGE\n  gitbay repo topics list [<owner/name>]", "SEE ALSO\n  gitbay repo topics add\n"} {
		if !strings.Contains(topics, want) {
			t.Errorf("missing %q in:\n%s", want, topics)
		}
	}
	if strings.Contains(topics, "topics list add") {
		t.Errorf("SEE ALSO renamed a child after the leaf:\n%s", topics)
	}
	noun := via(t, Term{Cols: 100}, "--path=auth keys", "help", "keys")
	for _, want := range []string{"USAGE\n  gitbay auth keys <verb> ...\n", "gitbay auth keys <verb> --help for flags.\n"} {
		if !strings.Contains(noun, want) {
			t.Errorf("missing %q in:\n%s", want, noun)
		}
	}
}

func TestHelpRendersAnAliasedNounWithTheRegistryLayout(t *testing.T) {
	var out, errOut bytes.Buffer
	c := &Ctx{Stdout: &out, Stderr: &errOut, Term: Term{Cols: 100}, Scope: "full", CLIPath: "auth"}
	c.Cfg.Server.SiteURL = "https://forge.test"
	if code := Dispatch(c, []string{"help", "auth"}); code != protocol.ExitOK {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	got := out.String()
	for _, want := range []string{"auth whoami", "auth keys list", "auth pgp add", "auth token create", "auth export"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "no command matches") {
		t.Errorf("auth did not resolve: %s", got)
	}
}

func TestHelpRendersAnAliasedNounInRegisteredFormOverSSH(t *testing.T) {
	got := helpOut(t, Term{}, "auth")
	for _, want := range []string{"whoami", "keys list", "pgp add", "token create", "account export"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "auth keys list") {
		t.Errorf("stock ssh should not see the CLI-only auth prefix: %s", got)
	}
}
