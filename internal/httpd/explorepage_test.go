package httpd

import (
	"strings"
	"testing"

	"gitbay.org/gitbay/internal/store"
	"gitbay.org/gitbay/internal/web"
)

// A filtered explore showed a shorter list and nothing else: no count, no
// restatement of the query, and no way back to the unfiltered view (#243).
func TestExplorePageSummarisesTheFilter(t *testing.T) {
	repos := []describedRepo{
		{Repo: store.Repo{OwnerName: "krz", Name: "gitbay"}, Desc: "A CLI-first git forge."},
	}
	out := renderExplore(t, "forge", repos)
	for _, want := range []string{"1 repository", "matching <strong>forge</strong>", `href="/explore"`, "clear filter"} {
		if !strings.Contains(out, want) {
			t.Errorf("explore.html missing %q:\n%s", want, out)
		}
	}

	// Nothing to clear when nothing is filtered.
	if out := renderExplore(t, "", repos); strings.Contains(out, "clear filter") {
		t.Errorf("unfiltered explore offers a clear:\n%s", out)
	} else if !strings.Contains(out, "1 repository") {
		t.Errorf("unfiltered explore has no count:\n%s", out)
	}

	// A filter that matched nothing says so rather than claiming the
	// instance has no public repositories.
	out = renderExplore(t, "nothing", nil)
	if !strings.Contains(out, "nothing matches that filter") {
		t.Errorf("empty filtered list reads as an empty instance:\n%s", out)
	}
	if !strings.Contains(out, "0 repositories") {
		t.Errorf("empty filtered list has no count:\n%s", out)
	}
}

func renderExplore(t *testing.T, q string, repos []describedRepo) string {
	t.Helper()
	var sb strings.Builder
	err := web.Render(&sb, "explore.html", struct {
		basePage
		Tab    string
		Query  string
		Facets []facetGroup
		Repos  []describedRepo
	}{basePage{Site: "gitbay"}, "explore", q, nil, repos})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	return sb.String()
}
