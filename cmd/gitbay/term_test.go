package main

import (
	"strings"
	"testing"
)

func TestTermValue(t *testing.T) {
	env := func(m map[string]string) func(string) string {
		return func(k string) string { return m[k] }
	}
	cases := []struct {
		tty     bool
		cols    int
		env     map[string]string
		noColor bool
		want    string
	}{
		{true, 120, nil, false, "120,color"},
		{false, 120, nil, false, ""},
		{true, 30, nil, false, ""},
		{true, 120, map[string]string{"NO_COLOR": "1"}, false, "120"},
		{true, 120, map[string]string{"TERM": "dumb"}, false, "120"},
		{true, 120, nil, true, "120"},
	}
	for _, c := range cases {
		noColor = c.noColor
		if got := termValue(c.tty, c.cols, env(c.env)); got != c.want {
			t.Errorf("%+v: got %q", c, got)
		}
	}
	noColor = false
}

func TestStripNoColor(t *testing.T) {
	args, ok := stripNoColor([]string{"gitbay", "issue", "list", "--no-color", "--state", "all"})
	if !ok || len(args) != 5 || args[3] != "--state" {
		t.Errorf("got %v %v", args, ok)
	}
}

func TestPagerArgv(t *testing.T) {
	env := func(m map[string]string) func(string) (string, bool) {
		return func(k string) (string, bool) { v, ok := m[k]; return v, ok }
	}
	cases := []struct {
		env  map[string]string
		want string
	}{
		{nil, "less"},
		{map[string]string{"PAGER": "more -s"}, "more -s"},
		{map[string]string{"PAGER": "more", "GITBAY_PAGER": "bat -p"}, "bat -p"},
		{map[string]string{"PAGER": "more", "GITBAY_PAGER": ""}, ""},
	}
	for _, c := range cases {
		if got := strings.Join(pagerArgv(env(c.env)), " "); got != c.want {
			t.Errorf("%v: got %q want %q", c.env, got, c.want)
		}
	}
}

func TestPages(t *testing.T) {
	yes := [][]string{{"issue", "show"}, {"mr", "diff"}, {"build", "log"}, {"repo", "log"}}
	for _, s := range yes {
		if !pages(s, nil) {
			t.Errorf("%v should page", s)
		}
	}
	if pages([]string{"build", "log"}, []string{"--follow"}) {
		t.Error("build log --follow must not page")
	}
	if pages([]string{"issue", "show"}, []string{"--json"}) {
		t.Error("--json must not page")
	}
	if pages([]string{"issue", "list"}, nil) {
		t.Error("list must not page")
	}
}
