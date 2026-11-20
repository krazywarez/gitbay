package codescan

import (
	"bufio"
	"bytes"
	"path"
	"slices"
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
	// Read returns a file at the scanned commit, for rules whose hits are
	// checked against the source. Nil checks nothing.
	Read func(path string) ([]byte, error)
	// Paths are the files at the scanned commit, for rules whose hits are
	// checked against the rest of the repository. Nil checks nothing.
	Paths []string
}

// maxPythonFiles bounds how many Python files a scope reads for the names
// they define.
const maxPythonFiles = 2000

// Aside counts the findings a scope set aside: Ignored by path or rule,
// Unused by the library a rule is about, Quoted by reading the template an
// unquoted-attribute hit is in, Unsplit by reading the script an
// unquoted-expansion hit is in, Joined by reading the Python file a
// string-concat-in-list hit is in, NoReturn by reading the line a return
// hit is on, Attributes by reading what the repository defines an
// is_ name as, InTests by the path of an insecure-random hit.
type Aside struct {
	Ignored    int            `json:"ignored,omitempty"`
	Unused     map[string]int `json:"unused,omitempty"`
	Quoted     int            `json:"quoted,omitempty"`
	Unsplit    int            `json:"unsplit,omitempty"`
	Joined     int            `json:"joined,omitempty"`
	NoReturn   int            `json:"no_return,omitempty"`
	Attributes int            `json:"attributes,omitempty"`
	InTests    int            `json:"in_tests,omitempty"`
}

// Any reports whether anything was set aside.
func (a Aside) Any() bool {
	return a.Ignored > 0 || len(a.Unused) > 0 || a.Quoted > 0 || a.Unsplit > 0 || a.Joined > 0 ||
		a.NoReturn > 0 || a.Attributes > 0 || a.InTests > 0
}

// Apply splits findings into those reported, at most MaxFindings of them,
// and counts of those set aside. It reports whether reported ones were
// dropped for the cap.
func (s Scope) Apply(findings []store.SecurityFinding) ([]store.SecurityFinding, Aside, bool) {
	var kept []store.SecurityFinding
	var aside Aside
	truncated := false
	unquoted, unsplit, joined := map[string]map[int]bool{}, map[string]map[int]bool{}, map[string]map[int]bool{}
	returns := map[string]map[int]bool{}
	var names *pyNames
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
		if ruleIn(f.Rule, unquotedAttrRule) && !s.onLine(f, unquoted, unquotedLines) {
			aside.Quoted++
			continue
		}
		if ruleIn(f.Rule, unquotedExpansionRule) && !s.onLine(f, unsplit, splitLines) {
			aside.Unsplit++
			continue
		}
		if ruleIn(f.Rule, joinedStringRule) && !s.onLine(f, joined, keepJoined) {
			aside.Joined++
			continue
		}
		if slices.ContainsFunc(returnRules, func(r string) bool { return ruleIn(f.Rule, r) }) && !s.onLine(f, returns, returnLines) {
			aside.NoReturn++
			continue
		}
		if ruleIn(f.Rule, attrCallRule) && s.attribute(f, &names) {
			aside.Attributes++
			continue
		}
		if ruleIn(f.Rule, insecureRandomRule) && testPath(f.Path) {
			aside.InTests++
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

// onLine reports whether a hit is on a line that lines, reading the file,
// marks, or the file cannot be read to say. Files are read once, into seen.
func (s Scope) onLine(f store.SecurityFinding, seen map[string]map[int]bool, lines func([]byte) (map[int]bool, bool)) bool {
	if s.Read == nil || f.Path == "" || f.Line == 0 {
		return true
	}
	marked, read := seen[f.Path]
	if !read {
		if src, err := s.Read(f.Path); err == nil {
			if l, ok := lines(src); ok {
				marked = l
			}
		}
		seen[f.Path] = marked
	}
	return marked == nil || marked[f.Line]
}

// attribute reports whether an is-function-without-parentheses hit names
// something the repository's Python files define as an attribute and never
// as a method. The files are read on the first such hit, into names.
func (s Scope) attribute(f store.SecurityFinding, names **pyNames) bool {
	m := attrCallName.FindStringSubmatch(f.Message)
	if m == nil || s.Read == nil || s.Paths == nil {
		return false
	}
	if *names == nil {
		n := pyNames{methods: map[string]bool{}, attrs: map[string]bool{}}
		read := 0
		for _, p := range s.Paths {
			if !strings.HasSuffix(p, ".py") || otherProject(p) {
				continue
			}
			if read == maxPythonFiles {
				break
			}
			read++
			if src, err := s.Read(p); err == nil {
				n.add(src)
			}
		}
		*names = &n
	}
	return (*names).attribute(m[1])
}

// otherProject reports whether a path is inside a directory of other
// projects' code or an environment.
func otherProject(p string) bool {
	for _, part := range strings.Split(path.Dir(p), "/") {
		switch part {
		case "node_modules", "vendor", "third_party", "site-packages", ".venv", "venv":
			return true
		}
	}
	return false
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
