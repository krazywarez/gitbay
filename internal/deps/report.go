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
		fmt.Fprintf(&b, "\n### %s\n\n", ecosystemNames[eco])
		cells := [][3]string{{"Package", "Current", "Latest"}}
		for _, r := range rows {
			cells = append(cells, [3]string{"`" + r.Name + "`", r.Current, r.Latest})
		}
		b.WriteString(table(cells))
	}
	return b.String()
}

// table pads every cell to its column, because the body is read twice: as
// a rendered table on the web, where the padding is ignored, and as the
// plain text of the notification mail, where it is the only thing keeping
// the columns readable. Row 0 is the header.
func table(cells [][3]string) string {
	var w [3]int
	for _, c := range cells {
		for i := range w {
			if n := len(c[i]); n > w[i] {
				w[i] = n
			}
		}
	}
	var b strings.Builder
	row := func(c [3]string) {
		fmt.Fprintf(&b, "| %-*s | %-*s | %-*s |\n", w[0], c[0], w[1], c[1], w[2], c[2])
	}
	row(cells[0])
	row([3]string{strings.Repeat("-", w[0]), strings.Repeat("-", w[1]), strings.Repeat("-", w[2])})
	for _, c := range cells[1:] {
		row(c)
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
