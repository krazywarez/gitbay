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
	"auth":          "whoami, SSH and PGP keys, email, API tokens",
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
	"query":         "saved issue and merge request queries across repositories",
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

// nounAlias is one bucket of registered commands, reachable under a
// CLI-only noun that is not itself a registry path (auth, gathering
// several unrelated registry prefixes): Registered is what runHelp
// matches against the registry, CLI is the path a gitbay caller
// actually types to reach it — not always Registered with the alias's
// own name stitched on (account export -> auth export drops a word),
// so the two are paired explicitly rather than derived.
type nounAlias struct {
	Registered string
	CLI        string
}

// nounAliases groups a CLI-only noun into the real prefixes it gathers,
// so `help auth` renders with the same layout a real noun gets instead
// of falling back to whatever a caller does when help fails. A stock
// ssh caller — the only one who could ever ask for a bare "auth" and
// get nothing back from the registry — sees the Registered forms
// unchanged; the CLI, having sent its own path, sees CLI.
var nounAliases = map[string][]nounAlias{
	"auth": {
		{"account export", "auth export"},
		{"whoami", "auth whoami"},
		{"keys", "auth keys"},
		{"email", "auth email"},
		{"pgp", "auth pgp"},
		{"token", "auth token"},
	},
}

// NounAliasNames returns the keys of nounAliases, for the CLI's own
// aliasGroupNames agreement test.
func NounAliasNames() []string {
	names := make([]string, 0, len(nounAliases))
	for name := range nounAliases {
		names = append(names, name)
	}
	return names
}

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
	prefixes := []string{prefix}
	override := map[string]string{}
	if aliased, ok := nounAliases[prefix]; ok {
		prefixes = nil
		for _, a := range aliased {
			prefixes = append(prefixes, a.Registered)
		}
		if c.CLIPath != "" {
			for _, cmd := range registry {
				p := joinPath(cmd.Path)
				for _, a := range aliased {
					if p == a.Registered || strings.HasPrefix(p, a.Registered+" ") {
						override[p] = a.CLI + strings.TrimPrefix(p, a.Registered)
						break
					}
				}
			}
		}
	}
	var matched []Command
	for _, cmd := range registry {
		p := joinPath(cmd.Path)
		for _, pfx := range prefixes {
			if pfx == "" || p == pfx || strings.HasPrefix(p, pfx+" ") {
				matched = append(matched, cmd)
				break
			}
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
			c.helpNoun(w, prefix, matched, override)
		}
	})
}

// viaCLI reports whether the caller is the gitbay CLI, as far as the
// session says: a terminal (only the CLI or a caller passing --term sets
// one), or a CLI path, which only the CLI sends.
func (c *Ctx) viaCLI() bool {
	return c.Term.Cols > 0 || c.CLIPath != ""
}

// program is how help spells the command it documents: gitbay for the
// CLI, ssh otherwise.
func (c *Ctx) program() string {
	if c.viaCLI() {
		return "gitbay"
	}
	return "ssh git@" + hostOf(c.Cfg.Server.SiteURL)
}

// cliUsage marks a leading <owner/name> optional in a usage line for the
// CLI, which infers it inside a clone (cmd/gitbay/ssh.go's withRepo).
// Stock ssh never does.
func cliUsage(usage string) string {
	return strings.Replace(usage, "<owner/name>", "[<owner/name>]", 1)
}

// shownAs rewrites full, which starts with the registered path, to start
// with the CLI's path instead when the CLI sent one that differs (#267).
// The CLI path must name this command: either it regroups it (the same
// last word, auth keys remove for keys remove) or extends it (repo
// topics list for repo topics). Arguments after a CLI command can
// dispatch to a longer registered path (gitbay repo topics list add
// reaches repo topics add), and that command keeps its own name.
func (c *Ctx) shownAs(registered, full string) string {
	rest, ok := strings.CutPrefix(full, registered)
	if !ok || c.CLIPath == "" || c.CLIPath == registered {
		return full
	}
	reg, cli := strings.Fields(registered), strings.Fields(c.CLIPath)
	if len(reg) == 0 || len(cli) == 0 || (cli[len(cli)-1] != reg[len(reg)-1] && !strings.HasPrefix(c.CLIPath, registered+" ")) {
		return full
	}
	return c.CLIPath + rest
}

// shownBelow is how another command listed beside registered prints to
// this caller. When the CLI only regrouped the command (auth keys remove
// for keys remove, the same last word) the other command takes the CLI's
// parent in place of the registered one. When the CLI renamed the last
// word (repo topics list for repo topics) nothing follows about the
// other command's name, so it keeps its registered path.
func (c *Ctx) shownBelow(registered, other string) string {
	reg, cli, o := strings.Fields(registered), strings.Fields(c.CLIPath), strings.Fields(other)
	if len(cli) == 0 || len(reg) == 0 || len(o) < len(reg) || cli[len(cli)-1] != reg[len(reg)-1] ||
		!slices.Equal(o[:len(reg)-1], reg[:len(reg)-1]) {
		return other
	}
	return joinPath(append(slices.Clip(cli[:len(cli)-1]), o[len(reg)-1:]...))
}

// usageShape is a usage line for the command registered at path as this
// caller should see it: the CLI's path in place of the registered one
// where they differ, and a leading <owner/name> optional for the CLI.
func (c *Ctx) usageShape(path []string, usage string) string {
	shape := c.shownAs(joinPath(path), usage)
	if c.viaCLI() {
		shape = cliUsage(shape)
	}
	return shape
}

// cmdUsage is the running command's usage with the program in front, as
// a usage refusal prints it.
func (c *Ctx) cmdUsage() string {
	return c.program() + " " + c.usageShape(c.Cmd.Path, c.Cmd.Usage)
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
	registered := joinPath(cmd.Path)
	shape := c.usageShape(cmd.Path, cmd.Usage)
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
			c.wrapLine(w, "  "+c.program()+" ", c.shownAs(registered, ex))
		}
	}
	if len(below) > 0 {
		fmt.Fprintln(w)
		c.heading(w, "SEE ALSO")
		for _, b := range below {
			fmt.Fprintf(w, "  %s %s\n", c.program(), c.shownBelow(registered, joinPath(b.Path)))
		}
	}
}

// helpNoun renders a noun with several commands under it. override, from
// an aliased noun (auth), gives the full CLI path for a row that is not
// itself under prefix (keys list, gathered under auth, becomes "auth
// keys list"); it is empty for an ordinary noun, so every row there
// still trims to just its own verb.
func (c *Ctx) helpNoun(w io.Writer, prefix string, cmds []Command, override map[string]string) {
	head := nounSummaries[strings.Fields(prefix)[0]]
	fmt.Fprintln(w, head)
	// An aliased noun is a CLI grouping: over stock ssh there is no
	// "auth <verb>" to type, and each row already names its full command.
	_, aliased := nounAliases[prefix]
	bare := aliased && c.CLIPath == ""
	display := c.shownAs(prefix, prefix)
	if !bare {
		fmt.Fprintln(w)
		c.heading(w, "USAGE")
		fmt.Fprintf(w, "  %s %s <verb> ...\n", c.program(), display)
	}
	rowText := func(cmd Command) string {
		full := joinPath(cmd.Path)
		if ov, ok := override[full]; ok {
			return ov
		}
		return strings.TrimPrefix(full, prefix+" ")
	}
	wide := 0
	for _, cmd := range cmds {
		wide = max(wide, cells(rowText(cmd)))
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
			fmt.Fprintf(w, "  %s  %s\n", pad(rowText(cmd), wide), cmd.Summary)
		}
	}
	if !bare {
		fmt.Fprintln(w)
		fmt.Fprintf(w, "%s %s <verb> --help for flags.\n", c.program(), display)
	}
}
