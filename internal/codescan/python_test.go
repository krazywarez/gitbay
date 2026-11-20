package codescan

import (
	"errors"
	"testing"

	"gitbay.org/gitbay/internal/store"
)

func TestJoinedOutsideLists(t *testing.T) {
	for _, tc := range []struct {
		name, src string
		line      int
		want      bool
	}{
		{"call in a comprehension", "rows = [\n    r[\"a\"]\n    for r in conn.execute(\n        \"select a \"\n        \"from t\",\n        (x,),\n    )\n]\n", 4, true},
		{"call in a list", "rules = [\n    Rule(\n        \"name\",\n        \"one message \"\n        \"over two lines\",\n    ),\n]\n", 4, true},
		{"tuple in a list", "[(\"a\" \"b\", 1)]\n", 1, true},
		{"list element", "names = [\n    \"alpha\"\n    \"beta\",\n]\n", 2, false},
		{"set element", "{\"a\" \"b\", \"c\"}\n", 1, false},
		{"one-element list", "raise Failed([\n    \"a message \"\n    \"over two lines\"\n])\n", 2, false},
		{"list and call on one line", "[\"a\" \"b\", f(\"c\" \"d\")]\n", 1, false},
		{"f-string with nested quotes", "[f(f\"{x[\"k\"]}\" \"y\")]\n", 1, true},
		{"triple-quoted", "[g(\"\"\"a\nb\"\"\" \"c\")]\n", 1, true},
		{"comment between", "[g(\"a\"  # note ]\n  \"b\")]\n", 1, true},
		{"no join on the line", "[g(\"a\")]\n", 1, false},
	} {
		lines, ok := joinedOutsideLists([]byte(tc.src))
		if !ok {
			t.Errorf("%s: not read", tc.name)
			continue
		}
		if lines[tc.line] != tc.want {
			t.Errorf("%s: line %d outside = %v, want %v (%v)", tc.name, tc.line, lines[tc.line], tc.want, lines)
		}
	}
	for _, src := range []string{`x = ["a`, `x = ("a" "b"`, "x = '''a", `x = f"{y"`, `x = )`} {
		if _, ok := joinedOutsideLists([]byte(src)); ok {
			t.Errorf("%q read", src)
		}
	}
}

func TestScopeChecksJoinedStrings(t *testing.T) {
	rule := "opt.opengrep-rules.python.lang.correctness.common-mistakes.string-concat-in-list"
	files := map[string]string{
		"call.py":   "x = [\n    f(\n        \"a \"\n        \"b\"\n    )\n]\n",
		"list.py":   "x = [\n    \"a\"\n    \"b\",\n]\n",
		"broken.py": "x = [f(\"a\" \"b\"\n",
	}
	read := func(p string) ([]byte, error) {
		if src, ok := files[p]; ok {
			return []byte(src), nil
		}
		return nil, errors.New("missing")
	}
	findings := []store.SecurityFinding{
		{Rule: rule, Path: "call.py", Line: 3},
		{Rule: rule, Path: "list.py", Line: 2},
		{Rule: rule, Path: "call.py", Line: 1},
		{Rule: rule, Path: "broken.py", Line: 1},
		{Rule: rule, Path: "gone.py", Line: 1},
		{Rule: "other", Path: "call.py", Line: 3},
	}
	kept, aside, _ := Scope{Read: read}.Apply(findings)
	if aside.Joined != 1 || len(kept) != 5 {
		t.Fatalf("kept %+v aside %+v", kept, aside)
	}
	for _, f := range kept {
		if f.Path == "call.py" && f.Line == 3 && f.Rule == rule {
			t.Errorf("joined-in-a-call hit kept")
		}
	}
	if kept, aside, _ := (Scope{}).Apply(findings); aside.Joined != 0 || len(kept) != 6 {
		t.Errorf("without Read: kept %d aside %+v", len(kept), aside)
	}
}
