package deps

import "testing"

func TestResolvedReadsLockfiles(t *testing.T) {
	got := found(t, Resolved(tree(map[string]string{
		"go.mod": `module x

require (
	github.com/a/direct v1.0.0
	github.com/a/indirect v0.2.0 // indirect
	github.com/a/replaced v1.0.0
)

replace github.com/a/replaced => ../replaced
`,
		"package-lock.json": `{"packages":{
			"": {"name":"app"},
			"node_modules/left-pad": {"version":"1.3.0"},
			"node_modules/a/node_modules/nested": {"version":"2.0.0"},
			"node_modules/linked": {"link": true}
		}}`,
		"package.json": `{"dependencies":{"ignored-when-locked":"1.0.0"}}`,
		"Cargo.lock": `
[[package]]
name = "serde"
version = "1.0.197"
source = "registry+https://github.com/rust-lang/crates.io-index"

[[package]]
name = "member"
version = "0.1.0"

[[package]]
name = "from-git"
version = "0.3.0"
source = "git+https://example.com/x.git#abc"
`,
		"uv.lock": `
[[package]]
name = "Requests"
version = "2.31.0"
source = { registry = "https://pypi.org/simple" }

[[package]]
name = "app"
version = "0.1.0"
source = { editable = "." }
`,
		"Package.resolved": `{"pins":[
			{"identity":"vapor","location":"https://github.com/vapor/vapor.git","state":{"version":"4.0.0"}},
			{"identity":"branchy","location":"https://github.com/x/branchy","state":{"branch":"main","revision":"abc"}}
		],"version":2}`,
		"App.xcodeproj/project.xcworkspace/xcshareddata/swiftpm/Package.resolved": `{"object":{"pins":[
			{"package":"Sparkle","repositoryURL":"git@github.com:sparkle-project/Sparkle.git","state":{"version":"2.6.0"}}
		]},"version":1}`,
	}), []string{"go.mod", "App.xcodeproj", "Sources"}))

	want := map[string]string{
		"go:github.com/a/direct":                   "v1.0.0",
		"go:github.com/a/indirect":                 "v0.2.0",
		"npm:left-pad":                             "1.3.0",
		"npm:nested":                               "2.0.0",
		"cargo:serde":                              "1.0.197",
		"pypi:requests":                            "2.31.0",
		"swift:github.com/vapor/vapor":             "4.0.0",
		"swift:github.com/sparkle-project/Sparkle": "2.6.0",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
	if len(got) != len(want) {
		t.Errorf("resolved %v, want exactly %v", got, want)
	}
}

// Without a lockfile only exact pins are sent: a range's floor is rarely
// what is installed, and would match advisories the build does not have.
func TestResolvedWithoutLockfilesKeepsExactPins(t *testing.T) {
	got := found(t, Resolved(tree(map[string]string{
		"package.json":     `{"dependencies":{"exact":"1.2.3","caret":"^1.2.3","tilde":"~1.2.3"}}`,
		"Cargo.toml":       "[dependencies]\npinned = \"=1.0.0\"\ncaret = \"1.0\"\n",
		"requirements.txt": "flask==3.0.0\nrequests>=2.31\nbare\n",
	}), nil))
	want := map[string]string{"npm:exact": "1.2.3", "cargo:pinned": "1.0.0", "pypi:flask": "3.0.0"}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
	if len(got) != len(want) {
		t.Errorf("resolved %v, want exactly %v", got, want)
	}
}

func TestSwiftURLName(t *testing.T) {
	for in, want := range map[string]string{
		"https://github.com/vapor/vapor.git": "github.com/vapor/vapor",
		"https://github.com/vapor/vapor":     "github.com/vapor/vapor",
		"git@github.com:vapor/vapor.git":     "github.com/vapor/vapor",
		"ssh://git@gitbay.org/krz/org-swift": "gitbay.org/krz/org-swift",
		"https://user@example.com/a/b.git/":  "example.com/a/b",
		"/Users/me/local-package":            "",
		"../sibling":                         "",
	} {
		if got := swiftURLName(in); got != want {
			t.Errorf("swiftURLName(%q) = %q, want %q", in, got, want)
		}
	}
}
