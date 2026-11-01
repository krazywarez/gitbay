package deps

import (
	"strings"
	"testing"

	"gitbay.org/gitbay/internal/store"
)

// The body is read as a rendered table on the web and as the plain text of
// the notification mail, so every row has to line up on its own.
func TestBodyAlignsColumns(t *testing.T) {
	body := Body("main", []store.DepReport{
		{Ecosystem: EcoGo, Name: "go.yaml.in/yaml/v3", Current: "v3.0.4", Latest: "v3.0.5"},
		{Ecosystem: EcoGo, Name: "golang.org/x/net", Current: "v0.57.0", Latest: "v0.58.0"},
		{Ecosystem: EcoNPM, Name: "react", Current: "18.2.0", Latest: "19.0.0"},
	})
	var pipes []int
	for _, line := range strings.Split(body, "\n") {
		if !strings.HasPrefix(line, "|") {
			pipes = append(pipes, -1) // section break
			continue
		}
		pipes = append(pipes, len(line))
	}
	// Within a run of table lines every line is the same length.
	var run []int
	for _, n := range append(pipes, -1) {
		if n == -1 {
			for _, w := range run {
				if w != run[0] {
					t.Fatalf("ragged table in:\n%s", body)
				}
			}
			run = nil
			continue
		}
		run = append(run, n)
	}
	if !strings.Contains(body, "| `golang.org/x/net`   | v0.57.0 | v0.58.0 |") {
		t.Errorf("row not padded to its column:\n%s", body)
	}
}

func TestAdvisoryBody(t *testing.T) {
	body := AdvisoryBody(AdvisoryReport{Repo: "krz/x", Branch: "main", Open: []store.DepAdvisory{
		{Ecosystem: EcoGo, Name: "golang.org/x/net", Version: "v0.1.0", ID: "GO-2", Summary: "HTTP/2 reset", Severity: "", Fixed: "0.7.0"},
		{Ecosystem: EcoPyPI, Name: "flask", Version: "2.0.0", ID: "GHSA-aaaa", Summary: "cookie leak", Severity: "high", Fixed: "2.2.5"},
	}, Dismissed: 2, NotImported: []store.DepAdvisory{{Ecosystem: EcoGo, Name: "golang.org/x/crypto", ID: "GO-3"}}})
	for _, want := range []string{
		"| `golang.org/x/net` | v0.1.0  | GO-2     |          | 0.7.0    |",
		"- GHSA-aaaa: cookie leak https://osv.dev/vulnerability/GHSA-aaaa",
		"### PyPI",
		"Not listed: 2 dismissed; for Go packages no file here imports: GO-3 (`golang.org/x/crypto`).",
		"repo deps dismiss krz/x <advisory>",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q:\n%s", want, body)
		}
	}
}

// An advisory naming packages is set aside unless one of them is imported;
// one naming none, or of another ecosystem, is left alone.
func TestMarkNotImported(t *testing.T) {
	matched := []store.DepAdvisory{
		{Ecosystem: EcoGo, ID: "GO-1", Imports: []string{"golang.org/x/crypto/openpgp", "golang.org/x/crypto/openpgp/armor"}},
		{Ecosystem: EcoGo, ID: "GO-2", Imports: []string{"golang.org/x/crypto/ssh"}},
		{Ecosystem: EcoGo, ID: "GO-3"},
		{Ecosystem: EcoNPM, ID: "GHSA-1", Imports: []string{"x"}},
	}
	MarkNotImported(matched, map[string]bool{"golang.org/x/crypto/ssh": true})
	for i, want := range []bool{true, false, false, false} {
		if matched[i].NotImported != want {
			t.Errorf("%s: NotImported = %v", matched[i].ID, matched[i].NotImported)
		}
	}
}
