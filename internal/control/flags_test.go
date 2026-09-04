package control

import (
	"strings"
	"testing"
)

func TestParseFlags(t *testing.T) {
	spec := flagSpec{Values: []string{"--title", "--file"}, Multi: []string{"--add"}, Bools: []string{"--private"}, MaxPos: 1, Usage: "cmd <owner/name> [--title <t>]"}
	f, err := parseFlags([]string{"a/b", "--title", "-x", "--add", "one", "--private", "--add", "two", "--file", "-"}, spec)
	if err != nil {
		t.Fatal(err)
	}
	if f.pos(0) != "a/b" || f.Value("--title") != "-x" || f.Value("--file") != "-" || !f.Has("--private") ||
		strings.Join(f.List("--add"), ",") != "one,two" || f.Has("--nope") || f.pos(1) != "" {
		t.Fatalf("parsed wrong: %+v", f)
	}
	for _, bad := range [][]string{
		{"a/b", "--bogus"},        // unknown flag
		{"a/b", "--title"},        // missing value
		{"a/b", "c/d"},            // too many positionals
		{"--private", "a/b", "x"}, // too many, flags first
	} {
		if _, err := parseFlags(bad, spec); err == nil || !strings.Contains(err.Error(), "usage: cmd") {
			t.Errorf("%v: err=%v", bad, err)
		}
	}
	// "--" ends flags; MaxPos -1 takes any number.
	f, err = parseFlags([]string{"--", "--title", "x"}, flagSpec{Values: []string{"--title"}, MaxPos: -1})
	if err != nil || strings.Join(f.Pos, " ") != "--title x" || f.Has("--title") {
		t.Fatalf("-- handling: %v %+v", err, f)
	}
}
