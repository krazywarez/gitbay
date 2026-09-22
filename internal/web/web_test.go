package web

import (
	"io/fs"
	"regexp"
	"strings"
	"testing"
)

// Every page template parses with the layout at start-up, so a broken
// template fails the process rather than the first visit to its page.
// Package init has already done the work; this asserts it covered every
// file (#116).
func TestEveryPageTemplateParses(t *testing.T) {
	names := Pages()
	if len(names) < 30 {
		t.Fatalf("only %d page templates parsed: %v", len(names), names)
	}
	for _, want := range []string{"tree.html", "blob.html", "mr.html", "issue.html", "dashboard.html", "registered.html"} {
		if _, ok := pages[want]; !ok {
			t.Errorf("%s not parsed", want)
		}
	}
}

// A th with no rule of its own falls back to the browser's centred default
// (#151). The classes that do left-align one are read out of the stylesheet
// rather than listed here, so a new rule does not need a test change.
func TestHeaderRowsAreLeftAligned(t *testing.T) {
	aligned := map[string]bool{}
	for _, rule := range strings.Split(string(StyleCSS), "}") {
		sel, decls, ok := strings.Cut(rule, "{")
		if !ok || !strings.Contains(decls, "text-align: left") || !strings.Contains(sel, "th") {
			continue
		}
		for _, class := range regexp.MustCompile(`\.([a-z-]+)`).FindAllStringSubmatch(sel, -1) {
			aligned[class[1]] = true
		}
	}
	if len(aligned) == 0 {
		t.Fatal("no left-aligning th rule found in the stylesheet")
	}

	classes := regexp.MustCompile(`class="([^"]*)"`)
	entries, err := fs.ReadDir(templateFS, "templates")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		b, err := templateFS.ReadFile("templates/" + e.Name())
		if err != nil {
			t.Fatal(err)
		}
		// The table and row tags a header sits under, plus the header row
		// itself: any one of them may carry the class.
		for _, head := range strings.Split(string(b), "<thead>")[1:] {
			open, _, _ := strings.Cut(head, "</tr>")
			before := strings.Split(string(b), "<thead>"+head)[0]
			if i := strings.LastIndex(before, "<table"); i >= 0 {
				open += before[i:]
			}
			hit := false
			for _, m := range classes.FindAllStringSubmatch(open, -1) {
				for _, c := range strings.Fields(m[1]) {
					hit = hit || aligned[c]
				}
			}
			if !hit {
				t.Errorf("%s: header row is not left-aligned by any rule: %.60s", e.Name(), open)
			}
		}
	}
}

// when names the zone rather than leaving an absolute time ambiguous, and
// passes an unparseable value through unchanged (#182).
func TestWhenNamesTheZone(t *testing.T) {
	got := funcs["when"].(func(string) string)("2026-09-12T02:18:07.123Z")
	if got != "2026-09-12 02:18 UTC" {
		t.Fatalf("when: %q", got)
	}
	if got := funcs["when"].(func(string) string)("not a time"); got != "not a time" {
		t.Fatalf("passthrough: %q", got)
	}
}

// TestMainWidthClass checks each template's source for the width define
// the spec assigns it (wide or bounded), and that a reading page defines
// none. The merge request page picks wide for its diff view, so it gets
// a per-view define instead of a fixed one.
func TestMainWidthClass(t *testing.T) {
	wide := map[string]bool{"tree.html": true, "blob.html": true, "blame.html": true, "log.html": true, "commit.html": true, "compare.html": true, "builds.html": true, "build.html": true, "search.html": true, "globalsearch.html": true, "edit.html": true, "dashboard.html": true, "issues.html": true, "mrs.html": true, "explore.html": true, "notifications.html": true, "settings.html": true, "account.html": true, "admin.html": true, "adminusers.html": true}
	bounded := map[string]bool{"landing.html": true, "fork.html": true, "login.html": true, "logout.html": true, "register.html": true, "registered.html": true, "new.html": true, "issuenew.html": true, "mrnew.html": true, "snippetnew.html": true, "privacy.html": true, "404.html": true}
	perView := map[string]string{"mr.html": `{{define "width"}}{{if eq .View "diff"}}wide{{else}}reading{{end}}{{end}}`}
	for _, name := range Pages() {
		src, err := TemplateSource(name)
		if err != nil {
			t.Fatal(err)
		}
		has := strings.Contains(src, `{{define "width"}}`)
		switch {
		case perView[name] != "" && !strings.Contains(src, perView[name]):
			t.Errorf("%s: want per-view width define %s", name, perView[name])
		case wide[name] && !strings.Contains(src, `{{define "width"}}wide{{end}}`):
			t.Errorf("%s: want width wide", name)
		case bounded[name] && !strings.Contains(src, `{{define "width"}}bounded{{end}}`):
			t.Errorf("%s: want width bounded", name)
		case !wide[name] && !bounded[name] && perView[name] == "" && has:
			t.Errorf("%s: defines a width but the spec calls it reading", name)
		}
	}
	layout, err := TemplateSource("layout.html")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(layout, `<main id="content" class="content {{template "width" .}}">`) {
		t.Error("layout.html: main does not carry the width block")
	}
	if !strings.Contains(layout, `{{define "width"}}reading{{end}}`) {
		t.Error("layout.html: no default width")
	}
}

// The rail carries no visible text, so every one of its controls has to
// name itself twice over: an aria-label for the accessibility tree and a
// visually hidden span for anything reading the DOM text, with the glyph
// itself hidden from both so it is never announced as a graphic. This
// parses layout.html, so an icon link that forgets one fails here rather
// than on the page.
func TestRailIconsAreLabelled(t *testing.T) {
	src, err := TemplateSource("layout.html")
	if err != nil {
		t.Fatal(err)
	}
	// A control names its glyph either through the icon partial or with an
	// svg of its own; the aria-hidden loop below covers both.
	glyph := func(s string) bool {
		return strings.Contains(s, `{{template "icon" `) || strings.Contains(s, "<svg")
	}
	// Every square in the strip comes from the raillink partial, so the
	// rule is checked once there and once for each control written out
	// in full: the More summary, Log out and Sign in.
	link := src[strings.Index(src, `{{define "raillink"}}`):]
	link = link[:strings.Index(link, "\n")] // the partial is one line
	for _, want := range []string{`aria-label="{{.Name}}"`, `<span class="vh">{{.Name}}</span>`} {
		if !strings.Contains(link, want) {
			t.Errorf("raillink partial missing %s", want)
		}
	}
	if !strings.Contains(link, `{{template "icon" .Icon}}`) {
		t.Error("raillink partial draws no glyph")
	}
	// A call site that names no icon or no name would render a square
	// with neither, which the partial alone cannot catch.
	for _, call := range regexp.MustCompile(`{{template "raillink" dict [^}]*}}`).FindAllString(src, -1) {
		for _, want := range []string{`"Icon" `, `"Name" `} {
			if !strings.Contains(call, want) {
				t.Errorf("raillink call missing %s: %.90s", want, call)
			}
		}
	}

	// Every railicon control in the file, wherever it sits and whatever
	// else its class carries — the strip, the More summary, and the
	// foot's Log out and Sign in. Log out used to be a button and had a
	// check of its own here; it is a link to the confirmation page now,
	// and this loop covers the foot either way. The More menu's own rows
	// are not icon controls — their visible text is their name.
	tagRe := regexp.MustCompile(`(?s)<a [^>]*class="[^"]*railicon.*?</a>|<summary [^>]*class="[^"]*railicon.*?</summary>|<button [^>]*class="[^"]*railicon.*?</button>`)
	controls := tagRe.FindAllString(src, -1)
	if len(controls) < 3 {
		t.Fatalf("found %d railicon controls in layout.html, want the rail's full set", len(controls))
	}
	for _, c := range controls {
		for _, want := range []string{`aria-label="`, `<span class="vh">`} {
			if !strings.Contains(c, want) {
				t.Errorf("railicon control missing %s: %.80s", want, c)
			}
		}
		if !glyph(c) {
			t.Errorf("railicon control draws no glyph: %.80s", c)
		}
	}

	// The foot holds controls of its own rather than raillink squares, so
	// check it is in what tagRe just scanned: a foot written with none
	// would pass the loop above by being empty.
	foot := src[strings.Index(src, `<div class="railfoot">`):]
	foot = foot[:strings.Index(foot, "</nav>")]
	if len(tagRe.FindAllString(foot, -1)) == 0 {
		t.Error("no railicon control in the rail foot")
	}

	// No decorative graphic anywhere in the layout is exposed, the brand
	// mark included: the link around it carries the name.
	for _, svg := range regexp.MustCompile(`<svg[^>]*>`).FindAllString(src, -1) {
		if !strings.Contains(svg, `aria-hidden="true"`) {
			t.Errorf("svg without aria-hidden: %s", svg)
		}
	}
}

// Every pre in the stylesheet scrolls sideways, which makes it a
// scrollable region: a keyboard reaches its content only if the element
// can take focus, so every authored pre carries tabindex="0" — the same
// attribute focusableBlocks puts on the pre blocks of rendered markup
// (#226).
func TestPreBlocksAreFocusable(t *testing.T) {
	if !regexp.MustCompile(`(?s)\npre \{[^}]*overflow-x: auto`).Match(StyleCSS) {
		t.Fatal("style.css no longer scrolls every pre; this test's premise is gone")
	}
	preRe := regexp.MustCompile(`<pre[^>]*>`)
	n := 0
	for _, name := range append(Pages(), "layout.html") {
		src, err := TemplateSource(name)
		if err != nil {
			t.Fatal(err)
		}
		for _, tag := range preRe.FindAllString(src, -1) {
			n++
			if !strings.Contains(tag, `tabindex="0"`) {
				t.Errorf("%s: %s is a scrollable region with no way to focus it", name, tag)
			}
		}
	}
	if n < 10 {
		t.Fatalf("found %d pre blocks in the templates, want the site's set", n)
	}
}

// A link mixed into other text is told apart by its underline. The rule
// covers running text; these are the other places a link sits in a line
// of text it has to be picked out of — a path, a heading, an empty-state
// note. Link and muted text are 1.07:1 apart in dark, so colour alone is
// no cue (#226).
func TestMixedTextLinksAreUnderlined(t *testing.T) {
	css := string(StyleCSS)
	for _, sel := range []string{".crumbs a", "h1 a", "li.empty a"} {
		re := regexp.MustCompile(`(?m)^[^{}\n]*` + regexp.QuoteMeta(sel) + `[^{}\n]*\{[^}]*text-decoration: underline`)
		if !re.MatchString(css) {
			t.Errorf("%s is not underlined", sel)
		}
	}
}
