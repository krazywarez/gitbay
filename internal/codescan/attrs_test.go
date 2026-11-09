package codescan

import (
	"errors"
	"testing"

	"gitbay.org/gitbay/internal/store"
)

func TestUnquotedLines(t *testing.T) {
	for _, tc := range []struct {
		name, src string
		want      bool
	}{
		{"quotes inside a quoted value", `<a href="/{{ page.url | replace("index.html", "") }}">x</a>`, false},
		{"quoted literal in a quoted value", `<meta property="og:image" content="{{ "og.png" | absolute }}">`, false},
		{"single quotes", `<a href='{{ x }}'>`, false},
		{"action adding a name", `<option value="off"{{if eq $mode "off"}} selected{{end}}>Off</option>`, false},
		{"handlebars block", `<input {{#if on}}checked{{/if}}>`, false},
		{"comment", `<p {{/* note */}} class="a">`, false},
		{"jinja statement", `<option {% if x %}selected{% endif %}>`, false},
		{"expression in text", `<p>{{ title }}</p>`, false},
		{"script body", "<script>if (a<b) { x = {{ y }} }</script>", false},
		{"html comment", `<!-- <a href={{ x }}> -->`, false},
		{"unquoted value", `<a href={{ x }}>`, true},
		{"unquoted after text", `<a href=/p/{{ x }}>`, true},
		{"standing in for attributes", `<div {{ attrs }}>`, true},
		{"output inside an action", `<option {{if .A}}value={{.V}}{{end}}>`, true},
		{"triple stash", `<a href={{{ x }}}>`, true},
	} {
		lines, ok := unquotedLines([]byte(tc.src))
		if !ok {
			t.Errorf("%s: not read", tc.name)
			continue
		}
		if lines[1] != tc.want {
			t.Errorf("%s: unquoted = %v, want %v", tc.name, lines[1], tc.want)
		}
	}
}

func TestUnquotedLinesAcrossLines(t *testing.T) {
	src := "<ul>\n<li\n  class=\"a\"\n  data-x={{ x }}\n  title=\"{{ \"t\" }}\">\n</ul>\n"
	lines, ok := unquotedLines([]byte(src))
	if !ok || !lines[4] || lines[5] || len(lines) != 1 {
		t.Errorf("lines = %v ok %v", lines, ok)
	}
	for _, src := range []string{`<a href="{{ x }}`, `<a href={{ x`, `<!-- open`} {
		if _, ok := unquotedLines([]byte(src)); ok {
			t.Errorf("%q read", src)
		}
	}
}

func TestScopeChecksUnquotedAttributes(t *testing.T) {
	rule := "opt.opengrep-rules.generic.html-templates.security.unquoted-attribute-var"
	files := map[string]string{
		"quoted.html":   "<p>\n<a href=\"{{ f(\"a\") }}\">x</a>\n",
		"unquoted.html": "<a href={{ x }}>\n",
		"broken.html":   "<a href=\"{{ x }}",
	}
	read := func(p string) ([]byte, error) {
		if src, ok := files[p]; ok {
			return []byte(src), nil
		}
		return nil, errors.New("missing")
	}
	findings := []store.SecurityFinding{
		{Rule: rule, Path: "quoted.html", Line: 2},
		{Rule: rule, Path: "unquoted.html", Line: 1},
		{Rule: rule, Path: "broken.html", Line: 1},
		{Rule: rule, Path: "gone.html", Line: 1},
		{Rule: "other", Path: "quoted.html", Line: 2},
	}
	kept, aside, _ := Scope{Read: read}.Apply(findings)
	if aside.Quoted != 1 || len(kept) != 4 {
		t.Fatalf("kept %+v aside %+v", kept, aside)
	}
	for _, f := range kept {
		if f.Path == "quoted.html" && f.Rule == rule {
			t.Errorf("quoted hit kept")
		}
	}
	if kept, aside, _ := (Scope{}).Apply(findings); aside.Quoted != 0 || len(kept) != 5 {
		t.Errorf("without Read: kept %d aside %+v", len(kept), aside)
	}
}
