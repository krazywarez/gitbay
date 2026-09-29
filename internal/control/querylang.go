package control

import (
	"fmt"
	"regexp"
	"strings"

	"gitbay.org/gitbay/internal/policy"
	"gitbay.org/gitbay/internal/store"
)

// ItemQuery is a parsed issue and merge request query (#292): terms
// separated by spaces, every term narrowing the result.
//
//	repo:owner/name  repo:owner/glob*  repo:*  owner:name
//	is:open|closed|merged  is:issue|mr
//	label:x (repeatable, all must match)  no:label
//	milestone:x  no:milestone
//	assignee:user|@me  author:user|@me
//	anything else without a colon: text matched against title and body
//
// Several repo: and owner: terms widen the scope to any of them. A value
// with spaces is written in double quotes, label:"needs review".
type ItemQuery struct {
	Scopes      []store.RepoScope
	AnyRepo     bool // repo:*
	Kind        string
	State       string
	Labels      []string
	NoLabel     bool
	Milestone   string
	NoMilestone bool
	Assignee    string
	Author      string
	Text        []string
}

// QueryError is a query that does not parse. Token is the term at fault.
type QueryError struct {
	Token string
	Msg   string
}

func (e *QueryError) Error() string { return fmt.Sprintf("query term %q: %s", e.Token, e.Msg) }

// repoGlob is a repository name that may carry * wildcards; names hold
// none of GLOB's other metacharacters, so * is the only one that reaches
// SQLite.
var repoGlob = regexp.MustCompile(`^[a-z0-9._*-]{1,64}$`)

// queryTokens splits s on whitespace, keeping double-quoted runs whole
// and dropping the quotes. quoted marks a token that opened with a quote:
// it is text even if it holds a colon.
func queryTokens(s string) (toks []string, quoted []bool, err error) {
	var cur strings.Builder
	in, have, startQuoted := false, false, false
	flush := func() {
		if have {
			toks = append(toks, cur.String())
			quoted = append(quoted, startQuoted)
		}
		cur.Reset()
		have, startQuoted = false, false
	}
	for _, r := range s {
		switch {
		case r == '"':
			if !have {
				startQuoted = true
			}
			in, have = !in, true
		case !in && (r == ' ' || r == '\t' || r == '\n' || r == '\r'):
			flush()
		default:
			cur.WriteRune(r)
			have = true
		}
	}
	if in {
		return nil, nil, &QueryError{Token: s, Msg: "unterminated quote"}
	}
	flush()
	return toks, quoted, nil
}

// ParseItemQuery parses the words of a query. Each word may itself hold
// several terms, so a query arrives the same whether it was one quoted
// argument or many.
func ParseItemQuery(words ...string) (ItemQuery, error) {
	var q ItemQuery
	n := 0
	for _, w := range words {
		toks, quoted, err := queryTokens(w)
		if err != nil {
			return q, err
		}
		for i, t := range toks {
			n++
			if err := q.term(t, quoted[i]); err != nil {
				return q, err
			}
		}
	}
	if n == 0 {
		return q, &QueryError{Token: "", Msg: "the query is empty"}
	}
	if text := strings.Join(q.Text, " "); text != "" {
		if err := validQuery(text); err != nil {
			return q, &QueryError{Token: text, Msg: "text " + err.Error()}
		}
	}
	return q, nil
}

func (q *ItemQuery) term(tok string, quoted bool) error {
	key, val, ok := strings.Cut(tok, ":")
	if quoted || !ok {
		q.Text = append(q.Text, tok)
		return nil
	}
	bad := func(msg string) error { return &QueryError{Token: tok, Msg: msg} }
	if val == "" {
		return bad("missing value")
	}
	user := func(dst *string, what string) error {
		if val != "@me" && policy.ValidateName(val) != nil {
			return bad("not a username")
		}
		if *dst != "" && *dst != val {
			return bad("only one " + what)
		}
		*dst = val
		return nil
	}
	switch key {
	case "repo":
		if val == "*" {
			q.AnyRepo = true
			return nil
		}
		owner, name, ok := strings.Cut(val, "/")
		if !ok || policy.ValidateName(owner) != nil || !repoGlob.MatchString(name) {
			return bad("want repo:owner/name, repo:owner/glob or repo:*")
		}
		q.Scopes = append(q.Scopes, store.RepoScope{Owner: owner, Name: name})
	case "owner":
		if policy.ValidateName(val) != nil {
			return bad("not an owner name")
		}
		q.Scopes = append(q.Scopes, store.RepoScope{Owner: val})
	case "is":
		switch val {
		case "issue", "mr":
			if q.Kind != "" && q.Kind != val {
				return bad("only one of is:issue and is:mr")
			}
			q.Kind = val
		case "open", "closed", "merged":
			if q.State != "" && q.State != val {
				return bad("only one of is:open, is:closed and is:merged")
			}
			q.State = val
		default:
			return bad("is: takes open, closed, merged, issue or mr")
		}
		if q.State == "merged" && q.Kind == "issue" {
			return bad("an issue is never merged")
		}
		if q.Kind == "mr" && q.Assignee != "" {
			return bad("merge requests have no assignees")
		}
	case "label":
		if !q.hasLabel(val) {
			q.Labels = append(q.Labels, val)
		}
		if q.NoLabel {
			return bad("label: and no:label never both match")
		}
	case "milestone":
		if (q.Milestone != "" && q.Milestone != val) || q.NoMilestone {
			return bad("only one milestone")
		}
		q.Milestone = val
	case "no":
		switch val {
		case "label":
			if len(q.Labels) > 0 {
				return bad("label: and no:label never both match")
			}
			q.NoLabel = true
		case "milestone":
			if q.Milestone != "" {
				return bad("only one milestone")
			}
			q.NoMilestone = true
		default:
			return bad("no: takes label or milestone")
		}
	case "assignee":
		if q.Kind == "mr" {
			return bad("merge requests have no assignees")
		}
		if err := user(&q.Assignee, "assignee"); err != nil {
			return err
		}
	case "author":
		return user(&q.Author, "author")
	default:
		return bad("unknown qualifier; the qualifiers are repo:, owner:, is:, label:, no:, milestone:, assignee: and author:")
	}
	if issues, mrs := q.Selects(); !issues && !mrs {
		return bad("is:merged and assignee: never both match: only merge requests merge, and they have no assignees")
	}
	return nil
}

func (q ItemQuery) hasLabel(l string) bool {
	for _, x := range q.Labels {
		if x == l {
			return true
		}
	}
	return false
}

// Selects reports which tables the query can match rows in.
func (q ItemQuery) Selects() (issues, mrs bool) {
	issues = q.Kind != "mr" && q.State != "merged"
	mrs = q.Kind != "issue" && q.Assignee == ""
	return
}

// String is the canonical text, which is what a saved query stores.
func (q ItemQuery) String() string {
	var t []string
	add := func(k, v string) {
		if strings.ContainsAny(v, " \t\r\n") {
			v = `"` + v + `"`
		}
		t = append(t, k+":"+v)
	}
	if q.AnyRepo {
		t = append(t, "repo:*")
	}
	for _, s := range q.Scopes {
		if s.Name == "" {
			add("owner", s.Owner)
		} else {
			add("repo", s.Owner+"/"+s.Name)
		}
	}
	if q.Kind != "" {
		add("is", q.Kind)
	}
	if q.State != "" {
		add("is", q.State)
	}
	for _, l := range q.Labels {
		add("label", l)
	}
	if q.NoLabel {
		add("no", "label")
	}
	if q.Milestone != "" {
		add("milestone", q.Milestone)
	}
	if q.NoMilestone {
		add("no", "milestone")
	}
	if q.Assignee != "" {
		add("assignee", q.Assignee)
	}
	if q.Author != "" {
		add("author", q.Author)
	}
	for _, w := range q.Text {
		if strings.Contains(w, ":") || strings.ContainsAny(w, " \t\r\n") {
			w = `"` + w + `"`
		}
		t = append(t, w)
	}
	return strings.Join(t, " ")
}

// Filter is the query as the store runs it for the user named me, over
// the tables issues and mrs allow as well as the query's own kind.
func (q ItemQuery) Filter(me string, issues, mrs bool) store.ItemFilter {
	qi, qm := q.Selects()
	f := store.ItemFilter{
		Issues: issues && qi, MRs: mrs && qm,
		State: q.State, Labels: q.Labels, NoLabel: q.NoLabel,
		Milestone: q.Milestone, NoMilestone: q.NoMilestone,
		Assignee: q.Assignee, Author: q.Author, Text: strings.Join(q.Text, " "),
	}
	if !q.AnyRepo {
		f.Scopes = q.Scopes
	}
	if f.Assignee == "@me" {
		f.Assignee = me
	}
	if f.Author == "@me" {
		f.Author = me
	}
	return f
}
