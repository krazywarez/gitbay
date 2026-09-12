package gitutil

import (
	"sort"
	"strconv"
	"strings"
)

// version parses "v1.2.3" or "1.2" into numeric parts. Anything else is
// not a version.
func version(name string) ([]int, bool) {
	s := strings.TrimPrefix(name, "v")
	if s == "" {
		return nil, false
	}
	var parts []int
	for _, p := range strings.Split(s, ".") {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return nil, false
		}
		parts = append(parts, n)
	}
	return parts, true
}

func versionLess(a, b []int) bool {
	for i := 0; i < len(a) && i < len(b); i++ {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return len(a) < len(b)
}

// SortVersions orders refs newest version first. Names that are not
// versions follow, by name.
func SortVersions(refs []Ref) {
	sort.SliceStable(refs, func(i, j int) bool {
		vi, oki := version(refs[i].Name)
		vj, okj := version(refs[j].Name)
		switch {
		case oki && okj:
			return versionLess(vj, vi)
		case oki != okj:
			return oki
		}
		return refs[i].Name < refs[j].Name
	})
}
