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

// pos is the nth positional argument, or "" when absent.
func (f flags) pos(n int) string {
	if n < len(f.Pos) {
		return f.Pos[n]
	}
	return ""
}
