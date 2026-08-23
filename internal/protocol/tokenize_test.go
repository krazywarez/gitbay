package protocol

import (
	"reflect"
	"strings"
	"testing"
)

func TestTokenize(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{`whoami --json`, []string{"whoami", "--json"}},
		{`repo create krz/newthing --private`, []string{"repo", "create", "krz/newthing", "--private"}},
		{`git-upload-pack '/krz/hutch.git'`, []string{"git-upload-pack", "/krz/hutch.git"}},
		{`issue create --title 'a b c'`, []string{"issue", "create", "--title", "a b c"}},
		{`issue create --title "a \"b\" c"`, []string{"issue", "create", "--title", `a "b" c`}},
		{`a\ b`, []string{"a b"}},
		{`'it''s'`, []string{"its"}},
		{`"don't"`, []string{"don't"}},
		{"  spaced \t out  ", []string{"spaced", "out"}},
		{`""`, []string{""}},
		{``, nil},
		{`--message "line1\nliteral"`, []string{"--message", `line1\nliteral`}},
	}
	for _, tc := range cases {
		got, err := Tokenize(tc.in)
		if err != nil {
			t.Errorf("Tokenize(%q) error: %v", tc.in, err)
			continue
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("Tokenize(%q) = %#v, want %#v", tc.in, got, tc.want)
		}
	}
}

func TestTokenizeRejects(t *testing.T) {
	bad := []string{
		`echo $(rm -rf /)`,
		"`id`",
		`a; b`,
		`a | b`,
		`a > f`,
		`a & b`,
		`'unterminated`,
		`"unterminated`,
		`trailing\`,
		`glob *`,
		`~root`,
	}
	for _, in := range bad {
		if got, err := Tokenize(in); err == nil {
			t.Errorf("Tokenize(%q) = %#v, want error", in, got)
		}
	}
}

// shellQuote quotes one word the way a POSIX client shell would.
func shellQuote(w string) string {
	return "'" + strings.ReplaceAll(w, "'", `'\''`) + "'"
}

// FuzzTokenizeRoundTrip checks that any argv, single-quoted as a client
// shell would emit it, tokenizes back to the identical argv.
func FuzzTokenizeRoundTrip(f *testing.F) {
	f.Add("whoami", "--json", "")
	f.Add("issue create", "--title", "a 'quoted' \"title\" with $pecial\\chars")
	f.Add("répo", "\t", "\n\n")
	f.Fuzz(func(t *testing.T, a, b, c string) {
		want := []string{a, b, c}
		quoted := make([]string, len(want))
		for i, w := range want {
			quoted[i] = shellQuote(w)
		}
		got, err := Tokenize(strings.Join(quoted, " "))
		if err != nil {
			t.Fatalf("Tokenize error on %q: %v", strings.Join(quoted, " "), err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("round trip: got %#v, want %#v", got, want)
		}
	})
}

// FuzzTokenizeNoPanic feeds arbitrary bytes; Tokenize must return, never panic.
func FuzzTokenizeNoPanic(f *testing.F) {
	f.Add(`repo create 'x`)
	f.Add(`\\\'\"`)
	f.Fuzz(func(t *testing.T, s string) {
		_, _ = Tokenize(s)
	})
}
