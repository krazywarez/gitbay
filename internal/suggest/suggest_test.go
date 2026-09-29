package suggest

import (
	"reflect"
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	cases := []struct {
		name  string
		body  string
		lines []string
		found bool
		err   string
	}{
		{"none", "just prose\n```go\nx\n```\n", nil, false, ""},
		{"one line", "try this\n```suggestion\nreturn nil\n```\n", []string{"return nil"}, true, ""},
		{"several", "```suggestion\na\n\tb\n```", []string{"a", "\tb"}, true, ""},
		{"deletion", "drop it\n```suggestion\n```\n", []string{}, true, ""},
		{"blank line", "```suggestion\n\n```\n", []string{""}, true, ""},
		{"crlf body", "x\r\n```suggestion\r\nnew\r\n```\r\n", []string{"new"}, true, ""},
		{"longer fence", "````suggestion\n```\n````\n", []string{"```"}, true, ""},
		{"unclosed", "```suggestion\nnew\n", nil, false, "not closed"},
		{"two", "```suggestion\na\n```\n```suggestion\nb\n```\n", nil, false, "one suggestion"},
	}
	for _, c := range cases {
		lines, found, err := Parse(c.body)
		if c.err != "" {
			if err == nil || !strings.Contains(err.Error(), c.err) {
				t.Errorf("%s: err = %v, want %q", c.name, err, c.err)
			}
			continue
		}
		if err != nil || found != c.found || (c.found && !reflect.DeepEqual(lines, c.lines)) {
			t.Errorf("%s: Parse = %q %v %v, want %q %v", c.name, lines, found, err, c.lines, c.found)
		}
	}
}

func TestStrip(t *testing.T) {
	if got := Strip("use this\n```suggestion\nx\n```\nthanks"); got != "use this\nthanks" {
		t.Errorf("Strip = %q", got)
	}
	if got := Strip("```suggestion\nx\n```"); got != "" {
		t.Errorf("Strip of a bare block = %q", got)
	}
}

func TestTextRoundTrip(t *testing.T) {
	for _, lines := range [][]string{nil, {""}, {"a"}, {"a", "", "b"}} {
		if got := FromText(Text(lines)); !reflect.DeepEqual(got, lines) && !(len(got) == 0 && len(lines) == 0) {
			t.Errorf("FromText(Text(%q)) = %q", lines, got)
		}
	}
}

func TestApply(t *testing.T) {
	cases := []struct {
		name       string
		content    string
		start, end int
		repl       []string
		want       string
		err        string
	}{
		{"one line", "a\nb\nc\n", 2, 2, []string{"B"}, "a\nB\nc\n", ""},
		{"range to more", "a\nb\nc\nd\n", 2, 3, []string{"x", "y", "z"}, "a\nx\ny\nz\nd\n", ""},
		{"range to fewer", "a\nb\nc\nd\n", 1, 3, []string{"x"}, "x\nd\n", ""},
		{"deletion", "a\nb\nc\n", 2, 2, nil, "a\nc\n", ""},
		{"delete all", "a\nb\n", 1, 2, nil, "", ""},
		{"eof no newline", "a\nb", 2, 2, []string{"B", "C"}, "a\nB\nC", ""},
		{"eof with newline", "a\nb\n", 2, 2, []string{"B"}, "a\nB\n", ""},
		{"crlf", "a\r\nb\r\nc\r\n", 2, 2, []string{"x", "y"}, "a\r\nx\r\ny\r\nc\r\n", ""},
		{"crlf eof no newline", "a\r\nb", 2, 2, []string{"x", "y"}, "a\r\nx\r\ny", ""},
		{"past eof", "a\nb\n", 2, 3, []string{"x"}, "", "has 2 lines"},
		{"bad range", "a\n", 2, 1, nil, "", "bad line range"},
	}
	for _, c := range cases {
		got, err := Apply([]byte(c.content), c.start, c.end, c.repl)
		if c.err != "" {
			if err == nil || !strings.Contains(err.Error(), c.err) {
				t.Errorf("%s: err = %v, want %q", c.name, err, c.err)
			}
			continue
		}
		if err != nil || string(got) != c.want {
			t.Errorf("%s: Apply = %q, %v; want %q", c.name, got, err, c.want)
		}
	}
}

func TestRange(t *testing.T) {
	if got, ok := Range([]byte("a\r\nb\nc"), 2, 3); !ok || string(got) != "b\nc" {
		t.Errorf("Range = %q %v", got, ok)
	}
	if _, ok := Range([]byte("a\n"), 1, 2); ok {
		t.Error("Range past the end reported ok")
	}
}
