package autolink

import (
	"strings"
	"testing"
)

// fakeResolver knows issue 4 and MR 2 in krz/gitbay, issue 7 in cmc/tools,
// and users alice and krz.
type fakeResolver struct{}

func (fakeResolver) RefURL(owner, name string, kind byte, n int64) string {
	switch {
	case owner == "krz" && name == "gitbay" && kind == '#' && n == 4:
		return IssueURL(owner, name, n)
	case owner == "krz" && name == "gitbay" && kind == '!' && n == 2:
		return MRURL(owner, name, n)
	case owner == "cmc" && name == "tools" && kind == '#' && n == 7:
		return IssueURL(owner, name, n)
	}
	return ""
}

func (fakeResolver) UserURL(name string) string {
	if name == "alice" || name == "krz" {
		return "/" + name
	}
	return ""
}

func rw(t *testing.T, in string) string {
	t.Helper()
	return Rewrite(in, "krz", "gitbay", fakeResolver{})
}

func TestRewrite(t *testing.T) {
	cases := []struct {
		name, in string
		want     []string // substrings that must appear
		wantNot  []string
	}{
		{"bare issue ref", "<p>see #4 for details</p>",
			[]string{`<a href="/krz/gitbay/issues/4" class="xref">#4</a>`}, nil},
		{"bare mr ref", "<p>fixed in !2.</p>",
			[]string{`<a href="/krz/gitbay/mrs/2" class="xref">!2</a>`}, nil},
		{"cross-repo ref", "<p>tracked at cmc/tools#7 upstream</p>",
			[]string{`<a href="/cmc/tools/issues/7" class="xref">cmc/tools#7</a>`}, nil},
		{"mention", "<p>ping @alice about it</p>",
			[]string{`<a href="/alice" class="xref">@alice</a>`}, nil},
		{"org mention", "<p>@krz owns this</p>",
			[]string{`<a href="/krz" class="xref">@krz</a>`}, nil},
		{"nonexistent issue stays text", "<p>see #999</p>",
			[]string{"<p>see #999</p>"}, []string{"<a"}},
		{"nonexistent user stays text", "<p>hi @nobody</p>",
			[]string{"<p>hi @nobody</p>"}, []string{"<a"}},
		{"code spans untouched", "<p>run <code>git show #4</code> now</p>",
			[]string{"<code>git show #4</code>"}, []string{`issues/4`}},
		{"pre blocks untouched", "<pre>#4 !2 @alice</pre>",
			[]string{"<pre>#4 !2 @alice</pre>"}, []string{"<a"}},
		{"existing links untouched", `<a href="/x">#4</a>`,
			[]string{`<a href="/x">#4</a>`}, []string{"issues/4"}},
		{"mid-word hash not a ref", "<p>sha a1b2#4 is odd</p>",
			nil, []string{"<a"}},
		{"mid-word at not a mention", "<p>mail me@alice.example ok</p>",
			nil, []string{"<a"}},
		{"multiple refs one line", "<p>#4 and !2 and @alice</p>",
			[]string{"issues/4", "mrs/2", `href="/alice"`}, nil},
		{"nested markup", "<ul><li>fixes #4</li><li><em>see !2</em></li></ul>",
			[]string{"issues/4", "mrs/2"}, nil},
		{"punctuation after ref", "<p>(#4), and #4.</p>",
			[]string{`class="xref">#4</a>),`}, nil},
		{"mention with trailing period", "<p>ask @alice.</p>",
			[]string{`class="xref">@alice</a>.`}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := rw(t, tc.in)
			for _, w := range tc.want {
				if !strings.Contains(got, w) {
					t.Errorf("missing %q in:\n%s", w, got)
				}
			}
			for _, w := range tc.wantNot {
				if strings.Contains(got, w) {
					t.Errorf("unexpected %q in:\n%s", w, got)
				}
			}
		})
	}
}

func TestRewriteEscaping(t *testing.T) {
	// Text around references must stay properly escaped after the
	// parse/render round trip.
	got := rw(t, "<p>x &lt;script&gt; #4 &amp; done</p>")
	if !strings.Contains(got, "&lt;script&gt;") || !strings.Contains(got, "&amp; done") {
		t.Fatalf("escaping lost:\n%s", got)
	}
	if !strings.Contains(got, "issues/4") {
		t.Fatalf("ref not linked:\n%s", got)
	}
}
