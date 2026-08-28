package policy

import (
	"reflect"
	"testing"
)

func TestCodeowners(t *testing.T) {
	rules := ParseCodeowners(`
# comment
*           @alice
*.go        @bob @carol
docs/       @dana
/deploy/    @erin
/cmd/gitbay/main.go @frank
internal/*  @grace
`)
	cases := []struct {
		path string
		want []string
	}{
		{"README.org", []string{"alice"}},
		{"x/y/z.txt", []string{"alice"}},
		{"main.go", []string{"bob", "carol"}},
		{"deep/nested/thing.go", []string{"bob", "carol"}},
		{"docs/users.org", []string{"dana"}},
		{"sub/docs/x.md", []string{"dana"}},          // unanchored dir matches anywhere
		{"deploy/cloud-init.yaml", []string{"erin"}}, // anchored dir
		{"cmd/gitbay/main.go", []string{"frank"}},    // exact anchored path beats *.go (later rule)
		{"internal/policy", []string{"grace"}},       // single-segment glob
	}
	for _, tc := range cases {
		if got := OwnersFor(rules, tc.path); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("OwnersFor(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
	// Later rules win: a .go file under docs/ belongs to dana, not bob.
	if got := OwnersFor(rules, "docs/gen.go"); !reflect.DeepEqual(got, []string{"dana"}) {
		t.Errorf("last-match-wins failed: %v", got)
	}
	// Anchored dir does not match nested occurrences.
	if got := OwnersFor(rules, "x/deploy/f"); !reflect.DeepEqual(got, []string{"alice"}) {
		t.Errorf("anchored dir leaked: %v", got)
	}
}
