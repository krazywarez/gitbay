package main

import (
	"slices"
	"strings"
	"testing"

	"github.com/spf13/cobra"
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
		{true, 120, map[string]string{"GITBAY_TERM": "off"}, false, ""},
		{true, 120, map[string]string{"COLORTERM": "truecolor"}, false, "120,color,truecolor"},
		{true, 120, map[string]string{"COLORTERM": "24bit", "NO_COLOR": "1"}, false, "120"},
		{true, 120, map[string]string{"TERM_PROGRAM": "iTerm.app"}, false, "120,color,links"},
		{true, 120, map[string]string{"TERM_PROGRAM": "iTerm.app", "GITBAY_LINKS": "0"}, false, "120,color"},
		{true, 120, map[string]string{"TERM_PROGRAM": "Apple_Terminal"}, false, "120,color"},
		{true, 120, map[string]string{"GITBAY_LINKS": "1"}, false, "120,color,links"},
		{true, 120, map[string]string{"VTE_VERSION": "7200"}, false, "120,color,links"},
		{true, 120, map[string]string{"COLORTERM": "truecolor", "GITBAY_LINKS": "1", "GITBAY_TERM": "basic"}, false, "120,color"},
	}
	for _, c := range cases {
		noColor = c.noColor
		if got := termValue(c.tty, c.cols, "", env(c.env)); got != c.want {
			t.Errorf("%+v: got %q", c, got)
		}
	}
	noColor = false
}

func TestTermValueHere(t *testing.T) {
	env := func(string) string { return "" }
	if got, want := termValue(true, 100, "krz/gitbay", env), "100,color,here=krz/gitbay"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if got := termValue(true, 100, "", env); strings.Contains(got, "here=") {
		t.Errorf("here= without a repository: %q", got)
	}
	if got := termValue(false, 100, "krz/gitbay", env); got != "" {
		t.Errorf("piped: %q", got)
	}
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

func TestCLIPathOf(t *testing.T) {
	root := &cobra.Command{Use: "gitbay"}
	auth := &cobra.Command{Use: "auth"}
	keys := &cobra.Command{Use: "keys"}
	remove := &cobra.Command{Use: "remove"}
	keys.AddCommand(remove)
	auth.AddCommand(keys)
	root.AddCommand(auth)
	if got := cliPathOf(remove); got != "auth keys remove" {
		t.Errorf("cliPathOf = %q", got)
	}
}

func TestWithCLIPath(t *testing.T) {
	argv := []string{"keys", "remove", "abc"}
	if got := withCLIPath("keys remove", "keys remove", argv); !slices.Equal(got, argv) {
		t.Errorf("matching path: %v", got)
	}
	if got := withCLIPath("", "keys remove", argv); !slices.Equal(got, argv) {
		t.Errorf("empty cliPath: %v", got)
	}
	got := withCLIPath("auth keys remove", "keys remove", argv)
	want := []string{"--path=auth keys remove", "keys", "remove", "abc"}
	if !slices.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}
