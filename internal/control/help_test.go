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
		"USAGE\n  gitbay issue list <owner/name> [flags]\n",
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
	if !strings.Contains(out, "USAGE\n  gitbay repo delete <owner/name> --yes\n") {
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
