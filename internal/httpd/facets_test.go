package httpd

import (
	"net/url"
	"testing"

	"gitbay.org/gitbay/internal/store"
)

// A facet link keeps every other active filter, sets its own, and clears
// its own when it is already active (desktop layout spec).
func TestFacetHrefKeepsOtherFilters(t *testing.T) {
	base := url.Values{"state": {"open"}, "label": {"bug"}, "q": {"crash"}}
	if got := facetHref(base, "milestone", "v2"); got != "?label=bug&milestone=v2&q=crash&state=open" {
		t.Errorf("set: %q", got)
	}
	if got := facetHref(base, "label", ""); got != "?q=crash&state=open" {
		t.Errorf("clear: %q", got)
	}
	if got := facetHref(base, "state", "closed"); got != "?label=bug&q=crash&state=closed" {
		t.Errorf("replace: %q", got)
	}
}

func TestListFacetsGroups(t *testing.T) {
	base := url.Values{"state": {"open"}, "label": {"bug"}}
	labels := []store.Label{{Name: "bug", Issues: 2, MRs: 1}, {Name: "docs", Issues: 0, MRs: 3}}
	ms := []store.Milestone{{Title: "v2", OpenItems: 4}}
	groups := listFacets(base, []string{"open", "closed", "all"}, "open", labels, ms, false)
	if len(groups) != 3 || groups[0].Title != "State" || groups[1].Title != "Labels" || groups[2].Title != "Milestones" {
		t.Fatalf("groups: %+v", groups)
	}
	st := groups[0].Items
	if !st[0].Active || st[0].Href != "?label=bug&state=open" || st[1].Active || st[1].Href != "?label=bug&state=closed" {
		t.Errorf("state items: %+v", st)
	}
	lb := groups[1].Items
	// docs has no issues, so the issue list does not offer it (#237)
	if len(lb) != 1 {
		t.Fatalf("labels: %+v", lb)
	}
	if lb[0].Label != "bug" || lb[0].Count != 2 || !lb[0].Active || lb[0].Href != "?state=open" {
		t.Errorf("active label clears itself: %+v", lb[0])
	}
	if m := groups[2].Items[0]; m.Label != "v2" || m.Count != 4 || m.Href != "?label=bug&milestone=v2&state=open" {
		t.Errorf("milestone: %+v", m)
	}
	// on the MR list a label's count is its MR count, and both labels
	// have merge requests, so both are offered
	mr := listFacets(base, []string{"open"}, "open", labels, nil, true)
	if len(mr[1].Items) != 2 || mr[1].Items[1].Count != 3 {
		t.Errorf("mr counts: %+v", mr[1].Items)
	}
}

// A count of zero is a link to an empty list, so it is left out — unless
// it is the filter in force, which has to stay clickable to clear (#237).
func TestListFacetsDropsEmpty(t *testing.T) {
	labels := []store.Label{{Name: "bug", Issues: 0}, {Name: "docs", Issues: 0}}
	ms := []store.Milestone{{Title: "v2", OpenItems: 0}, {Title: "v3", OpenItems: 1}}

	groups := listFacets(url.Values{"state": {"open"}}, []string{"open"}, "open", labels, ms, false)
	if n := len(groups[1].Items); n != 0 {
		t.Errorf("empty labels offered: %+v", groups[1].Items)
	}
	if n := len(groups[2].Items); n != 1 || groups[2].Items[0].Label != "v3" {
		t.Errorf("milestones: %+v", groups[2].Items)
	}

	base := url.Values{"state": {"open"}, "label": {"bug"}, "milestone": {"v2"}}
	groups = listFacets(base, []string{"open"}, "open", labels, ms, false)
	lb := groups[1].Items
	if len(lb) != 1 || lb[0].Label != "bug" || !lb[0].Active || lb[0].Href != "?milestone=v2&state=open" {
		t.Errorf("active empty label dropped: %+v", lb)
	}
	mg := groups[2].Items
	if len(mg) != 2 || mg[0].Label != "v2" || !mg[0].Active || mg[0].Href != "?label=bug&state=open" {
		t.Errorf("active empty milestone dropped: %+v", mg)
	}
}
