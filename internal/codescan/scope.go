package codescan

import (
	"bufio"
	"bytes"
	"path"
	"strings"

	"gitbay.org/gitbay/internal/ci"
	"gitbay.org/gitbay/internal/store"
)

// Scope is what a repository sets aside from what its scan reports.
type Scope struct {
	// Ignore are path globs, as CI paths take them (security-ignore).
	Ignore []string
	// Vendored are the .gitattributes patterns Vendored returns.
	Vendored []string
	// ExcludeRules are rule ids or families (security-exclude-rule).
	ExcludeRules []string
	// Uses reports whether the scanned commit depends on any of packages,
	// written as library lists them. Nil uses everything.
	Uses func(packages []string) bool
}

// Aside counts the findings a scope set aside: Ignored by path or rule,
// Unused by the library a rule is about.
type Aside struct {
	Ignored int            `json:"ignored,omitempty"`
	Unused  map[string]int `json:"unused,omitempty"`
}

// Apply splits findings into those reported, at most MaxFindings of them,
// and counts of those set aside. It reports whether reported ones were
// dropped for the cap.
func (s Scope) Apply(findings []store.SecurityFinding) ([]store.SecurityFinding, Aside, bool) {
	var kept []store.SecurityFinding
	var aside Aside
	truncated := false
	for _, f := range findings {
		if s.ignored(f) {
			aside.Ignored++
			continue
		}
		if l, ok := libraryFor(f.Rule); ok && s.Uses != nil && !s.Uses(l.packages) {
			if aside.Unused == nil {
				aside.Unused = map[string]int{}
			}
			aside.Unused[l.name()]++
			continue
		}
		if len(kept) == MaxFindings {
			truncated = true
			continue
		}
		kept = append(kept, f)
	}
	return kept, aside, truncated
}

func (s Scope) ignored(f store.SecurityFinding) bool {
	for _, g := range s.Ignore {
		if ci.Match(g, f.Path) {
			return true
		}
	}
	for _, p := range s.Vendored {
		if attrMatch(p, f.Path) {
			return true
		}
	}
	for _, r := range s.ExcludeRules {
		if ruleIn(f.Rule, r) {
			return true
		}
	}
	return false
}

// Vendored returns the patterns a .gitattributes file marks
// linguist-vendored or linguist-generated. Unset and false values are left
// out.
func Vendored(gitattributes []byte) []string {
	var out []string
	sc := bufio.NewScanner(bytes.NewReader(gitattributes))
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 2 || strings.HasPrefix(fields[0], "#") {
			continue
		}
		for _, a := range fields[1:] {
			if a == "linguist-vendored" || a == "linguist-generated" || a == "linguist-vendored=true" || a == "linguist-generated=true" {
				out = append(out, fields[0])
				break
			}
		}
	}
	return out
}

// attrMatch matches a path the way git matches a .gitattributes pattern: one
// without a slash names files by base name at any depth, one with a slash is
// relative to the top.
func attrMatch(pattern, file string) bool {
	if !strings.Contains(pattern, "/") {
		ok, err := path.Match(pattern, path.Base(file))
		return err == nil && ok
	}
	return ci.Match(strings.TrimPrefix(pattern, "/"), file)
}
