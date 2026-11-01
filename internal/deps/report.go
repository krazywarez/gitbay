package deps

import (
	"fmt"
	"sort"
	"strings"

	"gitbay.org/gitbay/internal/botissue"
	"gitbay.org/gitbay/internal/config"
	"gitbay.org/gitbay/internal/store"
)

// IssueTitle is fixed so the issue this worker maintains is recognizable
// across sweeps, in listings, and to the maintainer.
const IssueTitle = "Dependency updates available"

// AdvisoryTitle is the advisories issue's, fixed for the same reason.
const AdvisoryTitle = "Known vulnerabilities in dependencies"

// ecosystemNames are how the four appear in the issue body.
var ecosystemNames = map[string]string{
	EcoGo:    "Go",
	EcoNPM:   "npm",
	EcoCargo: "Cargo",
	EcoPyPI:  "PyPI",
	EcoSwift: "Swift",
}

// order is the sequence sections appear in.
var order = []string{EcoGo, EcoNPM, EcoCargo, EcoPyPI, EcoSwift}

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
		cells := [][]string{{"Package", "Current", "Latest"}}
		for _, r := range rows {
			cells = append(cells, []string{"`" + r.Name + "`", r.Current, r.Latest})
		}
		b.WriteString(table(cells))
	}
	return b.String()
}

// AdvisoryReport is what the advisories issue says: the advisories still
// open, how many were dismissed, and the Go ones set aside because no file
// imports the packages they name.
type AdvisoryReport struct {
	Repo, Branch string
	Open         []store.DepAdvisory
	Dismissed    int
	NotImported  []store.DepAdvisory
}

// AdvisoryBody renders the advisories issue: a table per ecosystem, then
// each advisory's summary and link, which would make the table too wide to
// read as plain text, then what is not listed.
func AdvisoryBody(r AdvisoryReport) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Dependencies resolved on `%s` match published advisories. "+
		"This issue is kept up to date as that changes, and closed once none are open.\n", r.Branch)
	byEco := map[string][]store.DepAdvisory{}
	for _, a := range r.Open {
		byEco[a.Ecosystem] = append(byEco[a.Ecosystem], a)
	}
	for _, eco := range order {
		rows := byEco[eco]
		if len(rows) == 0 {
			continue
		}
		fmt.Fprintf(&b, "\n### %s\n\n", ecosystemNames[eco])
		cells := [][]string{{"Package", "Version", "Advisory", "Severity", "Fixed in"}}
		for _, a := range rows {
			cells = append(cells, []string{"`" + a.Name + "`", a.Version, a.ID, a.Severity, a.Fixed})
		}
		b.WriteString(table(cells))
	}
	if len(r.Open) > 0 {
		b.WriteString("\n### Advisories\n\n")
		seen := map[string]bool{}
		for _, a := range r.Open {
			if seen[a.ID] {
				continue
			}
			seen[a.ID] = true
			fmt.Fprintf(&b, "- %s: %s https://osv.dev/vulnerability/%s\n", a.ID, a.Summary, a.ID)
		}
	}
	var notListed []string
	if r.Dismissed > 0 {
		notListed = append(notListed, fmt.Sprintf("%d dismissed", r.Dismissed))
	}
	if len(r.NotImported) > 0 {
		var ids []string
		for _, a := range r.NotImported {
			ids = append(ids, fmt.Sprintf("%s (`%s`)", a.ID, a.Name))
		}
		notListed = append(notListed, "for Go packages no file here imports: "+strings.Join(ids, ", "))
	}
	if len(notListed) > 0 {
		fmt.Fprintf(&b, "\nNot listed: %s. Imports through another module are not seen here. `repo deps status %s` shows them.\n",
			strings.Join(notListed, "; "), r.Repo)
	}
	if len(r.Open) > 0 {
		fmt.Fprintf(&b, "\nTo dismiss one: `repo deps dismiss %s <advisory> --reason not-affected|wont-fix|dev-only`.\n", r.Repo)
	}
	return b.String()
}

// MaintainAdvisories brings the advisories issue in line with a matching
// set: the open ones listed, the dismissed and not imported ones counted,
// closed when none are open.
func MaintainAdvisories(st *store.Store, cfg config.Config, repo store.Repo, matched []store.DepAdvisory,
	changed bool, save func() error) error {
	check, err := st.DepCheckFor(repo.ID)
	if err != nil {
		return err
	}
	dismissed, err := st.AdvisoryDismissals(repo.ID)
	if err != nil {
		return err
	}
	r := AdvisoryReport{Repo: repo.Path(), Branch: repo.DefaultBranch}
	for _, a := range matched {
		switch {
		case a.NotImported:
			r.NotImported = append(r.NotImported, a)
		case dismissed[a.ID].ID != "":
			r.Dismissed++
		default:
			r.Open = append(r.Open, a)
		}
	}
	return botissue.Maintain(st, cfg, repo, botissue.Tracked{
		Title:   AdvisoryTitle,
		Number:  check.AdvisoryIssueNumber,
		Record:  func(n int64) error { return st.SetDepAdvisoryIssue(repo.ID, n) },
		Empty:   len(r.Open) == 0,
		Changed: changed,
		Save:    save,
		Body:    func() string { return AdvisoryBody(r) },
	})
}

// table pads every cell to its column, because the body is read twice: as
// a rendered table on the web, where the padding is ignored, and as the
// plain text of the notification mail, where it is the only thing keeping
// the columns readable. Row 0 is the header.
func table(cells [][]string) string {
	w := make([]int, len(cells[0]))
	for _, c := range cells {
		for i := range w {
			if n := len(c[i]); n > w[i] {
				w[i] = n
			}
		}
	}
	var b strings.Builder
	row := func(c []string) {
		b.WriteString("|")
		for i := range w {
			fmt.Fprintf(&b, " %-*s |", w[i], c[i])
		}
		b.WriteString("\n")
	}
	row(cells[0])
	rule := make([]string, len(w))
	for i := range w {
		rule[i] = strings.Repeat("-", w[i])
	}
	row(rule)
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

// sameAdvisories reports whether two matching sets name the same
// advisories against the same versions. A summary or severity edited
// upstream does not count as a change worth a notification.
func sameAdvisories(a, b []store.DepAdvisory) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Ecosystem != b[i].Ecosystem || a[i].Name != b[i].Name ||
			a[i].Version != b[i].Version || a[i].ID != b[i].ID || a[i].Fixed != b[i].Fixed ||
			a[i].NotImported != b[i].NotImported {
			return false
		}
	}
	return true
}
