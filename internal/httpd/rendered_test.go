package httpd

import (
	"html/template"
	"strings"
	"testing"
)

func TestFocusableBlocks(t *testing.T) {
	got := string(focusableBlocks(`<pre class="chroma">x</pre><pre>y</pre><pre tabindex="-1">z</pre>`))
	want := `<pre tabindex="0" class="chroma">x</pre><pre tabindex="0">y</pre><pre tabindex="-1">z</pre>`
	if got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
}

func TestImageAlt(t *testing.T) {
	cases := map[string]string{
		`<a href="/b"><img src="https://forge.test/krz/gitbay/badge/build.svg"/></a>`: `<a href="/b"><img alt="build" src="https://forge.test/krz/gitbay/badge/build.svg"/></a>`,
		`<img src="pic.png?v=2">`:       `<img alt="pic" src="pic.png?v=2">`,
		`<img alt="" src="pic.png">`:    `<img alt="" src="pic.png">`,
		`<img alt="a chart" src="c.png">`: `<img alt="a chart" src="c.png">`,
		`<img>`:                         `<img alt="">`,
	}
	for in, want := range cases {
		if got := string(imageAlt(template.HTML(in))); got != want {
			t.Errorf("%s\n got %s\nwant %s", in, got, want)
		}
	}
}

// A README badge written as a bare org image link renders as a named
// image, and an org source block takes keyboard focus.
func TestOrgReadmeBadgeAndBlocks(t *testing.T) {
	out := string(renderReadme("README.org", []byte("[[https://forge.test/krz/gitbay/builds][https://forge.test/krz/gitbay/badge/build.svg]]\n\n#+begin_src go\npackage main\n#+end_src\n")))
	for _, want := range []string{`alt="build"`, `<pre tabindex="0"`} {
		if !strings.Contains(out, want) {
			t.Errorf("lacks %s:\n%s", want, out)
		}
	}
	if got := string(renderReadme("README.md", []byte("```go\npackage main\n```\n"))); !strings.Contains(got, `<pre tabindex="0"`) {
		t.Errorf("markdown fence has no tab stop:\n%s", got)
	}
}
