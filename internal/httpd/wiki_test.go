package httpd

import (
	"html/template"
	"reflect"
	"strings"
	"testing"

	"gitbay.org/gitbay/internal/store"
)

func TestWikiResolve(t *testing.T) {
	have := map[string]bool{"Admin": true, "Architecture/Identity": true, "Architecture/Deep/Page": true}
	exists := func(s string) bool { return have[s] }
	cases := []struct {
		page, link, want string
		ok               bool
	}{
		{"Home", "Admin", "Admin", true},
		{"Home", "Missing", "Missing", true},
		{"Home", "../etc/passwd", "", false},
		{"Architecture/Trust", "Identity", "Architecture/Identity", true},
		{"Architecture/Trust", "Admin", "Admin", true},
		{"Architecture/Trust", "Nowhere", "Architecture/Nowhere", true},
		{"Architecture/Trust", "../Admin", "Admin", true},
		{"Architecture/Trust", "Deep/Page", "Architecture/Deep/Page", true},
		{"Architecture/Deep/Page", "../Identity", "Architecture/Identity", true},
		{"Architecture/Trust", "../../x", "", false},
		{"", "Admin", "Admin", true},
	}
	for _, c := range cases {
		got, ok := wikiResolve(c.page, c.link, exists)
		if got != c.want || ok != c.ok {
			t.Errorf("wikiResolve(%q, %q) = %q, %v; want %q, %v", c.page, c.link, got, ok, c.want, c.ok)
		}
	}
}

func TestRewriteWikiLinksInSubfolder(t *testing.T) {
	p := repoPage{Repo: store.Repo{OwnerName: "krz", Name: "gitbay"}}
	pages := map[string]bool{"Admin": true, "Architecture/Identity": true}
	files := map[string]bool{"diagrams/a.svg": true, "Architecture/b.svg": true}
	in := template.HTML(`<a href="Identity.org">i</a><a href="Admin.org">a</a>` +
		`<a href="b.svg">diagram</a>` +
		`<img src="b.svg"><img src="diagrams/a.svg"><a href="https://x.test/">x</a>`)
	out := string(rewriteWikiLinks(in, p, "Architecture/Trust",
		func(s string) bool { return pages[s] }, func(s string) bool { return files[s] }))
	for _, want := range []string{
		`href="/krz/gitbay/wiki/Architecture/Identity"`,
		`href="/krz/gitbay/wiki/Admin"`,
		`href="/krz/gitbay/wiki/_raw/Architecture/b.svg"`,
		`src="/krz/gitbay/wiki/_raw/Architecture/b.svg"`,
		`src="/krz/gitbay/wiki/_raw/diagrams/a.svg"`,
		`href="https://x.test/"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %s in\n%s", want, out)
		}
	}
}

func TestWikiNav(t *testing.T) {
	got := wikiNav([]string{"API", "Architecture/Identity", "Architecture/Trust", "Home", "Ops/Backups"})
	want := []wikiNavGroup{
		{Dir: "", Pages: []wikiNavPage{{"API", "API"}, {"Home", "Home"}}},
		{Dir: "Architecture", Pages: []wikiNavPage{{"Architecture/Identity", "Identity"}, {"Architecture/Trust", "Trust"}}},
		{Dir: "Ops", Pages: []wikiNavPage{{"Ops/Backups", "Backups"}}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("wikiNav = %+v\nwant %+v", got, want)
	}
}
