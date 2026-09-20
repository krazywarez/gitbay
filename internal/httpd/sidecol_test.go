package httpd

import (
	"html/template"
	"net/url"
	"strings"
	"testing"

	"gitbay.org/gitbay/internal/store"
	"gitbay.org/gitbay/internal/web"
)

// renderIssues renders issues.html around the facets given, which is the
// shared sidecol partial every list page uses.
func renderIssues(t *testing.T, facets []facetGroup) string {
	t.Helper()
	var sb strings.Builder
	err := web.Render(&sb, "issues.html", struct {
		repoPage
		State       string
		Label       string
		Query       string
		Filters     []listFilter
		Facets      []facetGroup
		Issues      []store.Issue
		LabelColors map[string]template.CSS
		Older       string
	}{repoPage: testRepoPage(), State: "open", Facets: facets})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	return sb.String()
}

// The column's groups sit inside a details.sidedrop whose summary names
// the filters in force. Below 62rem that disclosure is the column; above
// it style.css hides the summary and shows the content, so the markup is
// the same either way (#237).
func TestSidecolIsADisclosure(t *testing.T) {
	base := url.Values{"state": {"open"}, "label": {"bug"}}
	labels := []store.Label{{Name: "bug", Issues: 2}}
	facets := listFacets(base, []string{"open", "closed"}, "open", labels, nil, false)

	out := renderIssues(t, facets)
	nav := strings.Index(out, `<nav class="sidecol" aria-label="Filters">`)
	drop := strings.Index(out, `<details class="sidedrop">`)
	sum := strings.Index(out, "<summary>")
	grp := strings.Index(out, `<div class="grp">`)
	if nav < 0 || drop < nav || sum < drop || grp < sum {
		t.Fatalf("nav=%d details=%d summary=%d grp=%d\n%s", nav, drop, sum, grp, out)
	}
	if !strings.Contains(out, `<span class="sideword">Filters</span>`) {
		t.Errorf("summary does not head with its word:\n%s", out)
	}
	// state and label are both in force, so both are named
	for _, want := range []string{`<span class="sideon">open</span>`, `<span class="sideon">bug</span>`} {
		if !strings.Contains(out, want) {
			t.Errorf("summary does not name %q:\n%s", want, out)
		}
	}
	if n := strings.Count(out, "</details>"); n != 1 {
		t.Errorf("details not closed once: %d", n)
	}
}

// The column leads the list in the markup, because below 62rem it is a
// control above what it narrows rather than a note after it (#237).
func TestSidecolPrecedesTheList(t *testing.T) {
	facets := listFacets(url.Values{"state": {"open"}}, []string{"open"}, "open", nil, nil, false)
	out := renderIssues(t, facets)
	if i, j := strings.Index(out, `class="sidecol"`), strings.Index(out, `class="colmain"`); i < 0 || j < i {
		t.Errorf("sidecol=%d colmain=%d", i, j)
	}
}
