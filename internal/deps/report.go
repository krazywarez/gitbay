package deps

import (
	"fmt"
	"sort"
	"strings"

	"gitbay.org/gitbay/internal/store"
)

// IssueTitle is fixed so the issue this worker maintains is recognizable
// across sweeps, in listings, and to the maintainer.
const IssueTitle = "Dependency updates available"

// ecosystemNames are how the four appear in the issue body.
var ecosystemNames = map[string]string{
	EcoGo:    "Go",
	EcoNPM:   "npm",
	EcoCargo: "Cargo",
	EcoPyPI:  "PyPI",
}

// order is the sequence sections appear in.
var order = []string{EcoGo, EcoNPM, EcoCargo, EcoPyPI}

// Body renders the issue: one table per ecosystem, sorted, so a diff
// between sweeps reads as a diff of what is behind.
func Body(branch string, reports []store.DepReport) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Dependencies declared on `%s` are behind their latest release. "+
		"This issue is kept up to date as that changes, and closed once nothing is behind.\n", branch)
	byEco := map[string][]store.DepReport{}
	for _, r := range reports {
		byEco[r.Ecosystem] = append(byEco[r.Ecosystem], r)
	}
	for _, eco := range order {
		rows := byEco[eco]
		if len(rows) == 0 {
			continue
		}
		sort.Slice(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })
		fmt.Fprintf(&b, "\n### %s\n\n| Package | Current | Latest |\n| --- | --- | --- |\n", ecosystemNames[eco])
		for _, r := range rows {
			fmt.Fprintf(&b, "| `%s` | %s | %s |\n", r.Name, r.Current, r.Latest)
		}
	}
	return b.String()
}

// same reports whether two outdated sets are equal, which is what decides
// between leaving the issue alone and rewriting it. Both sides arrive
// sorted by (ecosystem, name).
func same(a, b []store.DepReport) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
