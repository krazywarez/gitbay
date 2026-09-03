package httpd

import "net/url"

// listFilter is one active narrowing on a list page, with the URL that
// drops it and keeps the rest.
type listFilter struct {
	Key   string
	Value string
	Clear string
}

// activeFilters turns the non-empty (key, value) pairs into the chips a
// list page shows above its rows.
func activeFilters(state string, pairs [][2]string) []listFilter {
	var out []listFilter
	for i, kv := range pairs {
		if kv[1] == "" {
			continue
		}
		q := url.Values{"state": {state}}
		for j, other := range pairs {
			if j != i && other[1] != "" {
				q.Set(other[0], other[1])
			}
		}
		out = append(out, listFilter{Key: kv[0], Value: kv[1], Clear: "?" + q.Encode()})
	}
	return out
}
