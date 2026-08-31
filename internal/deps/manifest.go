package deps

import (
	"regexp"
	"sort"
	"strings"
)

// Ecosystems, in the order they are reported.
const (
	EcoGo    = "go"
	EcoNPM   = "npm"
	EcoCargo = "cargo"
	EcoPyPI  = "pypi"
)

// Dep is one direct dependency read from a manifest.
type Dep struct {
	Ecosystem string
	Name      string
	Current   string
}

// ReadFile returns a file from the tree being scanned, or an error when it
// is absent. Manifests are read from the repository root only; a monorepo
// with manifests in subdirectories is not scanned.
type ReadFile func(path string) ([]byte, error)

// MaxDeps bounds the work one repository can create for a sweep.
const MaxDeps = 300

// Scan returns the direct dependencies of every ecosystem whose manifest is
// present, capped at MaxDeps. Dependencies whose version cannot be pinned to
// an exact release — a range, a git or path source, a workspace member — are
// left out: there is nothing meaningful to compare them against.
func Scan(read ReadFile) []Dep {
	var out []Dep
	for _, parse := range []func(ReadFile) []Dep{parseGoMod, parseNPM, parseCargo, parsePython} {
		out = append(out, parse(read)...)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Ecosystem != out[j].Ecosystem {
			return out[i].Ecosystem < out[j].Ecosystem
		}
		return out[i].Name < out[j].Name
	})
	if len(out) > MaxDeps {
		out = out[:MaxDeps]
	}
	return out
}

// exactVersion matches a release we can compare: dot-separated numbers with
// an optional suffix, no range operators left in it. wildcard catches the
// ranges that survive that shape — "1.x", "2.*".
var (
	exactVersion = regexp.MustCompile(`^[0-9]+(\.[0-9]+)*([.\-+_a-zA-Z0-9]*)$`)
	wildcard     = regexp.MustCompile(`(^|\.)[xX*](\.|$)`)
)

// pin reduces a version requirement to the single release it names, or ""
// when it names a set rather than a release. A caret or tilde range pins its
// floor, which is the version actually recorded in the manifest; anything
// with alternatives, wildcards, or a non-registry source is skipped.
func pin(spec string) string {
	s := strings.TrimSpace(spec)
	if s == "" || strings.ContainsAny(s, "|,* ") {
		return ""
	}
	for _, bad := range []string{"workspace:", "npm:", "file:", "link:", "git", "http", "://"} {
		if strings.Contains(s, bad) {
			return ""
		}
	}
	s = strings.TrimLeft(s, "^~>=<")
	s = strings.TrimPrefix(s, "v")
	if s == "" || wildcard.MatchString(s) || !exactVersion.MatchString(s) {
		return ""
	}
	return s
}
