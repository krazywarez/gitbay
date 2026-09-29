package control

import (
	"errors"
	"reflect"
	"testing"

	"gitbay.org/gitbay/internal/store"
)

func TestParseItemQuery(t *testing.T) {
	for _, tc := range []struct {
		in   []string
		want ItemQuery
		text string // canonical form
	}{
		{[]string{"repo:krz/gitbay"}, ItemQuery{Scopes: []store.RepoScope{{Owner: "krz", Name: "gitbay"}}}, "repo:krz/gitbay"},
		{[]string{"repo:krz/git* owner:cmc"}, ItemQuery{Scopes: []store.RepoScope{{Owner: "krz", Name: "git*"}, {Owner: "cmc"}}}, "repo:krz/git* owner:cmc"},
		{[]string{"repo:*", "is:open"}, ItemQuery{AnyRepo: true, State: "open"}, "repo:* is:open"},
		{[]string{"is:merged"}, ItemQuery{State: "merged"}, "is:merged"},
		{[]string{"is:open is:issue"}, ItemQuery{Kind: "issue", State: "open"}, "is:issue is:open"},
		{[]string{"label:bug", "label:ui", "label:bug"}, ItemQuery{Labels: []string{"bug", "ui"}}, "label:bug label:ui"},
		{[]string{`label:"needs review"`}, ItemQuery{Labels: []string{"needs review"}}, `label:"needs review"`},
		{[]string{"label:needs review"}, ItemQuery{Labels: []string{"needs"}, Text: []string{"review"}}, "label:needs review"},
		{[]string{"no:label no:milestone"}, ItemQuery{NoLabel: true, NoMilestone: true}, "no:label no:milestone"},
		{[]string{"milestone:v2"}, ItemQuery{Milestone: "v2"}, "milestone:v2"},
		{[]string{"assignee:@me author:cmc"}, ItemQuery{Assignee: "@me", Author: "cmc"}, "assignee:@me author:cmc"},
		{[]string{"crash", `"on start"`, "is:open"}, ItemQuery{State: "open", Text: []string{"crash", "on start"}}, `is:open crash "on start"`},
		{[]string{`"a:b"`}, ItemQuery{Text: []string{"a:b"}}, `"a:b"`},
	} {
		got, err := ParseItemQuery(tc.in...)
		if err != nil {
			t.Errorf("%q: %v", tc.in, err)
			continue
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%q = %+v, want %+v", tc.in, got, tc.want)
		}
		if s := got.String(); s != tc.text {
			t.Errorf("%q canonical = %q, want %q", tc.in, s, tc.text)
		}
		again, err := ParseItemQuery(got.String())
		if err != nil || !reflect.DeepEqual(again, got) {
			t.Errorf("%q does not survive its canonical form: %+v, %v", tc.in, again, err)
		}
	}
}

func TestParseItemQueryRefuses(t *testing.T) {
	for _, tc := range []struct {
		in, token string
	}{
		{"", ""},
		{"foo:bar", "foo:bar"},
		{"is:open is:bogus", "is:bogus"},
		{"is:open is:closed", "is:closed"},
		{"is:issue is:mr", "is:mr"},
		{"is:issue is:merged", "is:merged"},
		{"is:mr assignee:cmc", "assignee:cmc"},
		{"assignee:cmc is:mr", "is:mr"},
		{"assignee:cmc assignee:bob", "assignee:bob"},
		{"author:Not_A_User", "author:Not_A_User"},
		{"repo:krz", "repo:krz"},
		{"repo:*/gitbay", "repo:*/gitbay"},
		{"repo:krz/[ab]", "repo:krz/[ab]"},
		{"owner:krz/x", "owner:krz/x"},
		{"label:bug no:label", "no:label"},
		{"no:milestone milestone:v1", "milestone:v1"},
		{"no:author", "no:author"},
		{"label:", "label:"},
		{`label:"open`, `label:"open`},
		{"x", "x"}, // text too short
	} {
		_, err := ParseItemQuery(tc.in)
		var qe *QueryError
		if !errors.As(err, &qe) {
			t.Errorf("%q: err %v, want a QueryError", tc.in, err)
			continue
		}
		if qe.Token != tc.token {
			t.Errorf("%q: token %q, want %q (%v)", tc.in, qe.Token, tc.token, err)
		}
	}
}

func TestItemQuerySelects(t *testing.T) {
	for _, tc := range []struct {
		in          string
		issues, mrs bool
	}{
		{"is:open", true, true},
		{"is:issue", true, false},
		{"is:mr", false, true},
		{"is:merged", false, true},
		{"assignee:cmc", true, false},
	} {
		q, err := ParseItemQuery(tc.in)
		if err != nil {
			t.Fatal(err)
		}
		if i, m := q.Selects(); i != tc.issues || m != tc.mrs {
			t.Errorf("%q selects issues=%v mrs=%v, want %v %v", tc.in, i, m, tc.issues, tc.mrs)
		}
	}
	q, _ := ParseItemQuery("repo:* repo:krz/x assignee:@me author:@me")
	f := q.Filter("alice", true, true)
	if f.Scopes != nil || f.Assignee != "alice" || f.Author != "alice" {
		t.Errorf("Filter = %+v: repo:* widens to every repository, @me is the caller", f)
	}
}
