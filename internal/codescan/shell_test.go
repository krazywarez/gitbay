package codescan

import (
	"errors"
	"testing"

	"gitbay.org/gitbay/internal/store"
)

func TestSplitLines(t *testing.T) {
	for _, tc := range []struct {
		name, src string
		want      bool
	}{
		{"arithmetic in double quotes", `text() { printf '×'; printf "%$(($1 - 1))s" '' | tr ' ' x; }`, false},
		{"bare arithmetic", `echo $(( $n + 1 ))`, false},
		{"nested arithmetic parens", `echo "$(( ($a + $b) * 2 ))"`, false},
		{"double quoted", `echo "$x ${y:-a b}"`, false},
		{"single quoted", `echo '$x'`, false},
		{"ansi-c quoted", `echo $'a\'$x'`, false},
		{"escaped dollar", `echo \$x`, false},
		{"special parameters", `echo $# $? $$ $! $-`, false},
		{"comment", `echo ok # $x`, false},
		{"comment line", "# the repo's own\necho \"$x\"", false},
		{"quoted heredoc", "cat <<'EOF'\n$(id) $x\nEOF", false},
		{"unquoted", `echo $x`, true},
		{"unquoted braces", `echo ${x}`, true},
		{"positional", `rm $1`, true},
		{"all arguments", `f $@`, true},
		{"command substitution in quotes", `echo "$(ls $dir)"`, true},
		{"command substitution in arithmetic", `echo $(( $(wc -l <$f) + 1 ))`, true},
		{"backticks in quotes", "echo \"`ls $dir`\"", true},
		{"after quoted arithmetic", `printf "%$(($1 - 1))s" $x`, true},
	} {
		lines, ok := splitLines([]byte(tc.src))
		if !ok {
			t.Errorf("%s: not read", tc.name)
			continue
		}
		if lines[1] != tc.want {
			t.Errorf("%s: split = %v, want %v", tc.name, lines[1], tc.want)
		}
	}
}

func TestSplitLinesAcrossLines(t *testing.T) {
	src := "a=\"$(($n\n+ 1))\"\necho \\\n  $x\ncat <<EOF\n$(id)\nEOF\necho \"$y\"\n"
	lines, ok := splitLines([]byte(src))
	want := map[int]bool{3: true, 4: true, 5: true, 6: true, 7: true}
	if !ok || len(lines) != len(want) {
		t.Fatalf("lines = %v ok %v", lines, ok)
	}
	for l := range want {
		if !lines[l] {
			t.Errorf("line %d not marked: %v", l, lines)
		}
	}
	for _, src := range []string{`echo "$x`, `echo '$x`, `echo $((1 + 2)`, `echo $(ls`, `echo ${x`, "cat <<EOF\nbody\n", `echo $'x`} {
		if _, ok := splitLines([]byte(src)); ok {
			t.Errorf("%q read", src)
		}
	}
}

func TestScopeChecksShellSplitting(t *testing.T) {
	rule := "opt.opengrep-rules.bash.lang.correctness.unquoted-variable-expansion-in-command"
	files := map[string]string{
		"quoted.sh":   "#!/bin/sh\nprintf \"%$(($1 - 1))s\" ''\n",
		"unquoted.sh": "rm $1\n",
		"broken.sh":   "echo \"$x\n",
	}
	read := func(p string) ([]byte, error) {
		if src, ok := files[p]; ok {
			return []byte(src), nil
		}
		return nil, errors.New("missing")
	}
	findings := []store.SecurityFinding{
		{Rule: rule, Path: "quoted.sh", Line: 2},
		{Rule: rule, Path: "unquoted.sh", Line: 1},
		{Rule: rule, Path: "broken.sh", Line: 1},
		{Rule: rule, Path: "gone.sh", Line: 1},
		{Rule: "other", Path: "quoted.sh", Line: 2},
	}
	kept, aside, _ := Scope{Read: read}.Apply(findings)
	if aside.Unsplit != 1 || len(kept) != 4 {
		t.Fatalf("kept %+v aside %+v", kept, aside)
	}
	for _, f := range kept {
		if f.Path == "quoted.sh" && f.Rule == rule {
			t.Errorf("unsplit hit kept")
		}
	}
	if kept, aside, _ := (Scope{}).Apply(findings); aside.Unsplit != 0 || len(kept) != 5 {
		t.Errorf("without Read: kept %d aside %+v", len(kept), aside)
	}
}
