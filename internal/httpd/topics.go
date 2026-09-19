package httpd

import (
	"net/url"
	"sort"
)

// topicFacets counts the topics across repos for explore's column. The
// links are the ones topic chips already use, ?q=<topic>; the active
// topic links to explore with no query.
func topicFacets(repos []describedRepo, q string) facetGroup {
	counts := map[string]int64{}
	for _, r := range repos {
		for _, t := range r.Topics {
			counts[t]++
		}
	}
	names := make([]string, 0, len(counts))
	for t := range counts {
		names = append(names, t)
	}
	sort.Slice(names, func(i, j int) bool {
		if counts[names[i]] != counts[names[j]] {
			return counts[names[i]] > counts[names[j]]
		}
		return names[i] < names[j]
	})
	if len(names) > 20 {
		names = names[:20]
	}
	g := facetGroup{Title: "Topics"}
	for _, t := range names {
		item := facetItem{Label: t, Count: counts[t], Href: "/explore?q=" + url.QueryEscape(t), Active: t == q}
		if item.Active {
			item.Href = "/explore"
		}
		g.Items = append(g.Items, item)
	}
	return g
}
