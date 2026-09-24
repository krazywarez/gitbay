package main

import "testing"

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
