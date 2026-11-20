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

func TestReturnLines(t *testing.T) {
	src := "TARGETS = {\n" +
		"    \"a\": lambda p: f\"x:{p['k']}\",\n" +
		"}\n" +
		"def f(x):\n" +
		"    return (\n" +
		"        x\n" +
		"    )\n" +
		"s = \"return\"  # return\n" +
		"returned = 1\n" +
		"class A:\n" +
		"    def __init__(self):\n" +
		"        self.k = sorted(r, key=lambda r: r[0])\n"
	lines, ok := returnLines([]byte(src))
	if !ok || len(lines) != 1 || !lines[5] {
		t.Errorf("lines = %v ok %v", lines, ok)
	}
	if _, ok := returnLines([]byte("x = 'open\n")); ok {
		t.Error("unclosed string read")
	}
}

func TestPyNames(t *testing.T) {
	n := pyNames{methods: map[string]bool{}, attrs: map[string]bool{}}
	n.add([]byte("@dataclass\nclass Actor:\n    is_admin: bool\n    is_owner: bool = False\n" +
		"    @property\n    def is_active(self):\n        return True\n" +
		"    @functools.cached_property\n    def is_cached(self):\n        return True\n" +
		"    def is_method(self):\n        return True\n" +
		"    async def is_async(self):\n        return True\n"))
	n.add([]byte("class B:\n    def __init__(self):\n        self.is_set = True\n" +
		"def f(user):\n    if user.is_used:\n        return user.is_used2\n" +
		"x.is_compared == 1\n"))
	for name, want := range map[string]bool{
		"is_admin": true, "is_owner": true, "is_active": true, "is_cached": true, "is_set": true,
		"is_method": false, "is_async": false, "is_used": false, "is_used2": false, "is_compared": false, "is_unknown": false,
	} {
		if got := n.attribute(name); got != want {
			t.Errorf("%s: attribute = %v, want %v", name, got, want)
		}
	}
	n.add([]byte("class C:\n    def is_admin(self):\n        return False\n"))
	if n.attribute("is_admin") {
		t.Error("is_admin is a method somewhere and still an attribute")
	}
}

func TestTestPath(t *testing.T) {
	for p, want := range map[string]bool{
		"Tests/OrgCoreTests/CalcTests.swift":      true,
		"Sources/App/AppTests.swift":              true,
		"internal/x/x_test.go":                    true,
		"tests/web/test_views.py":                 true,
		"pkg/test_util.py":                        true,
		"src/__tests__/a.js":                      true,
		"web/src/api.test.ts":                     true,
		"conftest.py":                             true,
		"Sources/CityAppKit/Audio/AudioMix.swift": false,
		"src/latest.py":                           false,
		"contest/main.go":                         false,
	} {
		if got := testPath(p); got != want {
			t.Errorf("%s: test = %v, want %v", p, got, want)
		}
	}
}

func TestScopeChecksPythonNamesReturnsAndTests(t *testing.T) {
	files := map[string]string{
		"app/users.py":   "@dataclass\nclass Actor:\n    is_admin: bool\n    def is_live(self):\n        return True\n",
		"app/records.py": "T = {\n    \"a\": lambda p: p,\n}\ndef f():\n    return 1\n",
		"vendor/x.py":    "def is_admin(self):\n    pass\n",
	}
	read := func(p string) ([]byte, error) {
		if src, ok := files[p]; ok {
			return []byte(src), nil
		}
		return nil, errors.New("missing")
	}
	attr := "opt.opengrep-rules.python.lang.maintainability.is-function-without-parentheses"
	ret := "opt.opengrep-rules.python.lang.maintainability.return-not-in-function"
	rnd := "opt.opengrep-rules.swift.lang.crypto.insecure-random"
	findings := []store.SecurityFinding{
		{Rule: attr, Path: "app/views.py", Line: 3, Message: `Is "is_admin" a function or an attribute? …`},
		{Rule: attr, Path: "app/views.py", Line: 4, Message: `Is "is_live" a function or an attribute? …`},
		{Rule: attr, Path: "app/views.py", Line: 5, Message: `Is "is_dir" a function or an attribute? …`},
		{Rule: ret, Path: "app/records.py", Line: 2},
		{Rule: ret, Path: "app/records.py", Line: 5},
		{Rule: rnd, Path: "Tests/CoreTests/CalcTests.swift", Line: 1},
		{Rule: rnd, Path: "Sources/Audio/Mix.swift", Line: 1},
	}
	paths := []string{"app/users.py", "app/records.py", "vendor/x.py", "README.md"}
	kept, aside, _ := Scope{Read: read, Paths: paths}.Apply(findings)
	if aside.Attributes != 1 || aside.NoReturn != 1 || aside.InTests != 1 || len(kept) != 4 {
		t.Fatalf("kept %+v aside %+v", kept, aside)
	}
	if kept, aside, _ := (Scope{Read: read}).Apply(findings); aside.Attributes != 0 || len(kept) != 5 {
		t.Errorf("without Paths: kept %d aside %+v", len(kept), aside)
	}
}
