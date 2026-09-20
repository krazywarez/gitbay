package httpd

import (
	"net/url"

	"gitbay.org/gitbay/internal/store"
)

// facetItem is one link in a list page's side column: a value the list
// narrows to. Clicking an active item clears it.
type facetItem struct {
	Label  string
	Count  int64
	Href   string
	Active bool
}

// facetGroup is one heading in the column: State, Labels, Milestones.
type facetGroup struct {
	Title string
	Items []facetItem
}

// facetHref returns "?..." with every parameter of base kept, key set to
// value, or dropped when value is "". url.Values encodes sorted, so the
// tests and the links agree byte for byte.
func facetHref(base url.Values, key, value string) string {
	q := url.Values{}
	for k, vs := range base {
		if k == key || len(vs) == 0 || vs[0] == "" {
			continue
		}
		q.Set(k, vs[0])
	}
	if value != "" {
		q.Set(key, value)
	}
	return "?" + q.Encode()
}

// listFacets builds the issue or merge request list's column from the
// active parameters, the states the page offers, and the repository's
// labels and open milestones. Counts are the rows' own: a label's issue
// count on the issue list, its MR count on the MR list. A count of zero
// is left out — it links to an empty list — unless it is the filter in
// force, which stays so it can be cleared (#237).
func listFacets(base url.Values, states []string, state string, labels []store.Label, ms []store.Milestone, forMRs bool) []facetGroup {
	var st facetGroup
	st.Title = "State"
	for _, s := range states {
		st.Items = append(st.Items, facetItem{Label: s, Href: facetHref(base, "state", s), Active: s == state})
	}
	lb := facetGroup{Title: "Labels"}
	for _, l := range labels {
		n := l.Issues
		if forMRs {
			n = l.MRs
		}
		active := base.Get("label") == l.Name
		if n == 0 && !active {
			continue
		}
		href := facetHref(base, "label", l.Name)
		if active {
			href = facetHref(base, "label", "")
		}
		lb.Items = append(lb.Items, facetItem{Label: l.Name, Count: n, Href: href, Active: active})
	}
	mg := facetGroup{Title: "Milestones"}
	for _, m := range ms {
		active := base.Get("milestone") == m.Title
		if m.OpenItems == 0 && !active {
			continue
		}
		href := facetHref(base, "milestone", m.Title)
		if active {
			href = facetHref(base, "milestone", "")
		}
		mg.Items = append(mg.Items, facetItem{Label: m.Title, Count: int64(m.OpenItems), Href: href, Active: active})
	}
	return []facetGroup{st, lb, mg}
}
