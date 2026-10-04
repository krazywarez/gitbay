package httpd

import (
	"fmt"
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
	p := explorePage{basePage: basePage{Site: "gitbay"}, Tab: "explore", Query: q}
	p.Repos, p.Total, p.Page, p.Pages = pageOf(repos, "")
	return renderExplorePage(t, p)
}

func renderExplorePage(t *testing.T, p explorePage) string {
	t.Helper()
	var sb strings.Builder
	if err := web.Render(&sb, "explore.html", p); err != nil {
		t.Fatalf("render: %v", err)
	}
	return sb.String()
}

func TestExplorePages(t *testing.T) {
	var repos []describedRepo
	for i := range 45 {
		repos = append(repos, describedRepo{Repo: store.Repo{OwnerName: "krz", Name: fmt.Sprintf("r%02d", i)}})
	}
	for _, tc := range []struct {
		in        string
		page, len int
		first     string
	}{
		{"", 1, 20, "r00"}, {"2", 2, 20, "r20"}, {"3", 3, 5, "r40"},
		{"9", 3, 5, "r40"}, {"0", 1, 20, "r00"}, {"x", 1, 20, "r00"},
	} {
		got, total, page, pages := pageOf(repos, tc.in)
		if total != 45 || pages != 3 || page != tc.page || len(got) != tc.len || got[0].Name != tc.first {
			t.Errorf("page %q: total %d, page %d of %d, %d rows from %s", tc.in, total, page, pages, len(got), got[0].Name)
		}
	}
	if got, _, page, pages := pageOf(nil, "2"); len(got) != 0 || page != 1 || pages != 1 {
		t.Errorf("empty listing: %d rows, page %d of %d", len(got), page, pages)
	}

	p := explorePage{basePage: basePage{Site: "gitbay"}, Tab: "explore", Query: "go"}
	p.Repos, p.Total, p.Page, p.Pages = pageOf(repos, "2")
	p.Prev, p.Next = explorePageURL("go", 1), explorePageURL("go", 3)
	out := renderExplorePage(t, p)
	for _, want := range []string{"45 repositories", "page 2 of 3", `href="/explore?q=go"`, `href="/explore?page=3&amp;q=go"`} {
		if !strings.Contains(out, want) {
			t.Errorf("explore.html missing %q:\n%s", want, out)
		}
	}
	if out := renderExplore(t, "", repos[:3]); strings.Contains(out, `class="pager"`) {
		t.Errorf("a single page has a pager:\n%s", out)
	}
}
