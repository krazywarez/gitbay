package store

import "strings"

// FTSQuery turns what someone typed into an FTS5 MATCH expression.
//
// FTS5's query language is not free text: bare `-`, `*`, `:`, `(`, `NOT`
// and an odd number of quotes are all syntax, and a syntax error surfaces
// as a failed query rather than no results. Nobody searching for "c++" or
// "foo: bar" means any of that. Every whitespace-separated run becomes a
// quoted phrase, which FTS5 treats as a literal and joins with an
// implicit AND, so the result is "rows containing all of these words".
func FTSQuery(q string) string {
	var terms []string
	for _, f := range strings.Fields(q) {
		// A double quote inside a phrase is escaped by doubling it.
		f = strings.ReplaceAll(f, `"`, `""`)
		terms = append(terms, `"`+f+`"`)
	}
	if len(terms) == 0 {
		// Matches nothing, rather than being a syntax error. A caller
		// should not run a search for an empty query, but if one does the
		// answer is no rows, not a failure.
		return `""`
	}
	return strings.Join(terms, " ")
}
