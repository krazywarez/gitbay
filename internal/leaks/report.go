package leaks

import (
	"fmt"
	"strings"

	"gitbay.org/gitbay/internal/ci"
)

// Modes a repository's secret scan runs in. The empty setting is
// ModeWarn: scanning sends nothing anywhere and warning blocks nothing, so
// it is on unless an admin turns it off.
const (
	ModeOff   = "off"
	ModeWarn  = "warn"
	ModeBlock = "block"
)

// Mode resolves a repository's setting.
func Mode(setting string) string {
	if setting == "" {
		return ModeWarn
	}
	return setting
}

// Allowed drops findings under a path the repository allows, given as CI
// path globs (`testdata/**`, `*.pem`).
func Allowed(findings []Finding, allow []string) []Finding {
	if len(allow) == 0 {
		return findings
	}
	var out []Finding
	for _, f := range findings {
		skip := false
		for _, g := range allow {
			if ci.Match(g, f.Path) {
				skip = true
				break
			}
		}
		if !skip {
			out = append(out, f)
		}
	}
	return out
}

// listed is how many findings a message names; the count covers the rest.
const listed = 20

// Describe lists findings for a person: one line each, commit, path, line
// and rule. heading leads it.
func Describe(heading string, findings []Finding, truncated bool) string {
	var b strings.Builder
	b.WriteString(heading)
	for i, f := range findings {
		if i == listed {
			fmt.Fprintf(&b, "\n  and %d more", len(findings)-listed)
			break
		}
		fmt.Fprintf(&b, "\n  %.10s %s:%d %s", f.Commit, f.Path, f.Line, f.Rule)
	}
	if truncated {
		b.WriteString("\n  (scan stopped at its size limit; not every file was read)")
	}
	return b.String()
}

// Plural is "1 possible secret" or "n possible secrets".
func Plural(n int) string {
	if n == 1 {
		return "1 possible secret"
	}
	return fmt.Sprintf("%d possible secrets", n)
}
