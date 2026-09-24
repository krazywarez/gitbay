package control

import (
	"fmt"
	"io"
	"slices"
	"strings"

	"gitbay.org/gitbay/internal/protocol"
	"gitbay.org/gitbay/internal/termtext"
)

func init() {
	register(Command{
		Path:     []string{"help"},
		Summary:  "list available commands",
		Usage:    "help [<prefix>...]",
		Examples: []string{"help", "help repo"},
		ReadOnly: true,
		Run:      runHelp,
	})
}

// nounSummaries is one line per distinct first path element in the
// registry, reusing the CLI's group short texts (cmd/gitbay/main.go)
// where the noun matches, so the two agree.
var nounSummaries = map[string]string{
	"account":       "export or import your account, for instance migration",
	"admin":         "instance administration (admins)",
	"audit":         "instance audit log (admins)",
	"build":         "CI builds",
	"dashboard":     "pinned repos, open MRs, assigned issues, recent builds",
	"email":         "manage email addresses",
	"explore":       "public repositories on this instance",
	"feed":          "activity on repositories you can reach",
	"help":          "list available commands",
	"issue":         "issues",
	"keys":          "manage SSH keys",
	"label":         "issue labels",
	"milestone":     "group issues and MRs toward a release",
	"mr":            "merge requests",
	"notifications": "your notification inbox",
	"org":           "organizations",
	"pgp":           "manage OpenPGP keys",
	"profile":       "user and org profiles",
	"register":      "create an account on this instance",
	"release":       "tag-anchored releases with notes and assets",
	"repo":          "create and manage repositories",
	"runner":        "the claim/report loop CI runners use",
	"search":        "find repositories, issues and merge requests",
	"snippet":       "shared text files, outside any repository",
	"status":        "commit statuses (CI)",
	"token":         "API tokens (minted over SSH, used with the JSON API)",
	"web":           "browser session",
	"webhook":       "outbound event delivery",
	"whoami":        "show the authenticated account",
	"wiki":          "a repository's wiki pages",
}

// NounSummaries returns nounSummaries, for the CLI group-text agreement
// test (task 4.5).
func NounSummaries() map[string]string { return nounSummaries }

// helpEntry is one row of the registry as help reports it.
type helpEntry struct {
	Path     string   `json:"path"`
	Summary  string   `json:"summary"`
	Usage    string   `json:"usage"`
	Flags    []Flag   `json:"flags,omitempty"`
	Examples []string `json:"examples,omitempty"`
}

// runHelp lists the registry, sorted, so a noun's commands sit together.
// Bare `help` stays one line per command. A prefix that names one command
// exactly renders its full USAGE/FLAGS/EXAMPLES; a prefix that names a
// noun with several commands under it renders a READ/WRITE summary.
func runHelp(c *Ctx, args []string) int {
	prefix := joinPath(args)
	var matched []Command
	for _, cmd := range registry {
		p := joinPath(cmd.Path)
		if prefix == "" || p == prefix || strings.HasPrefix(p, prefix+" ") {
			matched = append(matched, cmd)
		}
	}
	if len(matched) == 0 {
		return c.fail(protocol.ExitNotFound, "no command matches %q; try: help", prefix)
	}
	slices.SortFunc(matched, func(a, b Command) int { return strings.Compare(joinPath(a.Path), joinPath(b.Path)) })
	entries := make([]helpEntry, len(matched))
	for i, cmd := range matched {
		entries[i] = helpEntry{Path: joinPath(cmd.Path), Summary: cmd.Summary, Usage: cmd.Usage, Flags: cmd.Flags, Examples: cmd.Examples}
	}
	return c.emit(entries, func(w io.Writer) {
		switch {
		case prefix == "":
			for _, e := range entries {
				summary := e.Summary
				if c.Term.Cols > 0 {
					if avail := c.Term.Cols - max(cells(e.Path), 24) - 1; avail > 0 {
						summary = clip(summary, avail)
					}
				}
				fmt.Fprintf(w, "%-24s %s\n", e.Path, summary)
			}
		case joinPath(matched[0].Path) == prefix:
			c.helpVerb(w, matched[0], matched[1:])
		default:
			c.helpNoun(w, prefix, matched)
		}
	})
}

// program is how help spells the command it documents: the CLI at a
// terminal (only the CLI or a caller passing --term sets one), ssh
// otherwise.
func (c *Ctx) program() string {
	if c.Term.Cols > 0 {
		return "gitbay"
	}
	return "ssh git@" + hostOf(c.Cfg.Server.SiteURL)
}

func (c *Ctx) heading(w io.Writer, s string) {
	fmt.Fprintln(w, c.Term.paint(sgrBold, s))
}

// wrapLine prints prefix+text, wrapping text at Term.Cols with a hanging
// indent under prefix when it would overflow. Plain output never wraps.
func (c *Ctx) wrapLine(w io.Writer, prefix, text string) {
	if c.Term.Cols <= 0 || cells(prefix+text) <= c.Term.Cols {
		fmt.Fprintln(w, prefix+text)
		return
	}
	indent := cells(prefix)
	width := c.Term.Cols - indent
	for i, line := range termtext.Wrap(text, width) {
		if i == 0 {
			fmt.Fprintln(w, prefix+line)
		} else {
			fmt.Fprintln(w, strings.Repeat(" ", indent)+line)
		}
	}
}

func (c *Ctx) helpVerb(w io.Writer, cmd Command, below []Command) {
	fmt.Fprintln(w, cmd.Summary)
	fmt.Fprintln(w)
	c.heading(w, "USAGE")
	// Cutting at the first optional flag drops the rest of the usage
	// syntax behind "[flags]" — safe only for what is actually optional.
	// A required flag (repo delete --yes) or an alternative
	// (notifications read <id>... | --all) has no " [--" to cut at, so
	// the usage prints whole.
	shape := cmd.Usage
	if i := strings.Index(shape, " [--"); i >= 0 {
		shape = shape[:i] + " [flags]"
	}
	fmt.Fprintf(w, "  %s %s\n", c.program(), shape)
	fmt.Fprintln(w)
	c.heading(w, "FLAGS")
	rows := make([][2]string, 0, len(cmd.Flags)+1)
	for _, f := range cmd.Flags {
		name := f.Name
		if f.Arg != "" {
			name += " " + f.Arg
		}
		desc := f.Desc
		if f.Default != "" {
			desc += " (default " + f.Default + ")"
		}
		rows = append(rows, [2]string{name, desc})
	}
	rows = append(rows, [2]string{"--json", "machine-readable output"})
	wide := 0
	for _, r := range rows {
		wide = max(wide, cells(r[0]))
	}
	for _, r := range rows {
		c.wrapLine(w, "  "+pad(r[0], wide)+"  ", r[1])
	}
	if len(cmd.Examples) > 0 {
		fmt.Fprintln(w)
		c.heading(w, "EXAMPLES")
		for _, ex := range cmd.Examples {
			c.wrapLine(w, "  "+c.program()+" ", ex)
		}
	}
	if len(below) > 0 {
		fmt.Fprintln(w)
		c.heading(w, "SEE ALSO")
		for _, b := range below {
			fmt.Fprintf(w, "  %s %s\n", c.program(), joinPath(b.Path))
		}
	}
}

func (c *Ctx) helpNoun(w io.Writer, prefix string, cmds []Command) {
	head := nounSummaries[strings.Fields(prefix)[0]]
	fmt.Fprintln(w, head)
	fmt.Fprintln(w)
	c.heading(w, "USAGE")
	fmt.Fprintf(w, "  %s %s <verb> ...\n", c.program(), prefix)
	wide := 0
	for _, cmd := range cmds {
		wide = max(wide, cells(strings.TrimPrefix(joinPath(cmd.Path), prefix+" ")))
	}
	for _, section := range []struct {
		title string
		read  bool
	}{{"READ", true}, {"WRITE", false}} {
		first := true
		for _, cmd := range cmds {
			if cmd.ReadOnly != section.read {
				continue
			}
			if first {
				fmt.Fprintln(w)
				c.heading(w, section.title)
				first = false
			}
			verb := strings.TrimPrefix(joinPath(cmd.Path), prefix+" ")
			fmt.Fprintf(w, "  %s  %s\n", pad(verb, wide), cmd.Summary)
		}
	}
	fmt.Fprintln(w)
	fmt.Fprintf(w, "%s %s <verb> --help for flags.\n", c.program(), prefix)
}
