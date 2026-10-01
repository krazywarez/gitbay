package control

import (
	"fmt"
	"strings"
)

// flagSpec is what a command accepts: flags that take one value, flags
// that take a value and may repeat, switches, and how many positional
// arguments are allowed (-1 for any number). Every command used to walk
// argv by hand and decided on its own whether an unknown --flag was an
// error, a positional or nothing at all (#96); parseFlags decides once.
type flagSpec struct {
	Values []string
	Multi  []string
	Bools  []string
	MaxPos int
	Usage  string
}

// flags is a parsed argv: positionals in order, and each flag by name.
type flags struct {
	Pos   []string
	vals  map[string]string
	multi map[string][]string
	seen  map[string]bool
}

// Value is the last value given for a value flag, or "".
func (f flags) Value(name string) string { return f.vals[name] }

// Has reports whether a flag of any kind was given.
func (f flags) Has(name string) bool { return f.seen[name] }

// List is every value given for a repeatable flag, in order.
func (f flags) List(name string) []string { return f.multi[name] }

// parseFlags reads args against spec. A flag must be one the spec names;
// a value flag consumes the next argument verbatim, "-" included; "--"
// ends flag parsing. The error, when there is one, is the usage message.
func parseFlags(args []string, spec flagSpec) (flags, error) {
	f := flags{vals: map[string]string{}, multi: map[string][]string{}, seen: map[string]bool{}}
	kind := map[string]byte{}
	for _, n := range spec.Values {
		kind[n] = 'v'
	}
	for _, n := range spec.Multi {
		kind[n] = 'm'
	}
	for _, n := range spec.Bools {
		kind[n] = 'b'
	}
	usage := func(format string, a ...any) error {
		msg := fmt.Sprintf(format, a...)
		if spec.Usage != "" {
			msg += "\nusage: " + strings.TrimPrefix(spec.Usage, "usage: ")
		}
		return fmt.Errorf("%s", msg)
	}
	onlyPos := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !onlyPos && a == "--" {
			onlyPos = true
			continue
		}
		if !onlyPos && strings.HasPrefix(a, "--") {
			switch kind[a] {
			case 'b':
				f.seen[a] = true
			case 'v', 'm':
				if i+1 >= len(args) {
					return f, usage("%s requires a value", a)
				}
				f.seen[a] = true
				if kind[a] == 'v' {
					f.vals[a] = args[i+1]
				} else {
					f.multi[a] = append(f.multi[a], args[i+1])
				}
				i++
			default:
				if near := nearestFlag(a, kind); near != "" {
					return f, usage("unknown flag %q; did you mean %s?", a, near)
				}
				return f, usage("unknown flag %q", a)
			}
			continue
		}
		if spec.MaxPos >= 0 && len(f.Pos) >= spec.MaxPos {
			return f, usage("unexpected argument %q", a)
		}
		f.Pos = append(f.Pos, a)
	}
	return f, nil
}

// nearestFlag is the known flag closest to an unknown one: the only
// flag it is a prefix of, or else the only one within two edits.
func nearestFlag(a string, known map[string]byte) string {
	var prefixed, close []string
	for n := range known {
		if strings.HasPrefix(n, a) {
			prefixed = append(prefixed, n)
		}
		if editDistance(a, n) <= 2 {
			close = append(close, n)
		}
	}
	switch {
	case len(prefixed) == 1:
		return prefixed[0]
	case len(prefixed) == 0 && len(close) == 1:
		return close[0]
	}
	return ""
}

// editDistance is the Levenshtein distance between two ASCII strings.
func editDistance(a, b string) int {
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(b)]
}

// parseArgs is parseFlags for the running command, with the usage line
// printed the way a usage refusal prints it (cmdUsage): the program in
// front and the CLI's own path where it differs (#267). spec.Usage stays
// the text, since some commands spell their flags out more fully there
// than in the registered Usage.
func (c *Ctx) parseArgs(args []string, spec flagSpec) (flags, error) {
	usage := strings.TrimPrefix(spec.Usage, "usage: ")
	spec.Usage = ""
	f, err := parseFlags(args, spec)
	if err != nil && usage != "" {
		err = fmt.Errorf("%v\nusage: %s %s", err, c.program(), c.usageShape(c.Cmd.Path, usage))
	}
	return f, err
}

// pos is the nth positional argument, or "" when absent.
func (f flags) pos(n int) string {
	if n < len(f.Pos) {
		return f.Pos[n]
	}
	return ""
}
