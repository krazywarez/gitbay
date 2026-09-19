package httpd

import (
	"strings"
	"testing"

	"gitbay.org/gitbay/internal/control"
	"gitbay.org/gitbay/internal/web"
)

// The repository header is two rows: identity with the description and
// the buttons, then the tabs. The toggles hint is title text on the
// buttons, not a line of its own (desktop layout spec).
func TestRepoHeaderTwoRows(t *testing.T) {
	var sb strings.Builder
	p := testRepoPage()
	p.Viewer = "alice"
	p.Desc = "A CLI-first git forge."
	p.Topics = []string{"cli"}
	p.Tab = "files"
	err := web.Render(&sb, "builds.html", struct {
		repoPage
		Builds      []control.BuildOut
		Jobs        []control.JobOut
		Runs        []buildRun
		Filter      buildFilter
		FilterLinks []buildFilterLink
		Facets      []facetGroup
		Refs        []string
		CanWrite    bool
		Notice      string
	}{p, nil, nil, nil, buildFilter{}, nil, nil, nil, true, ""})
	if err != nil {
		t.Fatal(err)
	}
	out := sb.String()
	if strings.Contains(out, `class="toggles"`) || strings.Contains(out, "Pinned shows on your dashboard.") {
		t.Error("the toggles hint still renders as a line")
	}
	for _, want := range []string{
		`title="Pinned repositories show on your dashboard"`,
		`title="Watching sends every issue, request and build to your inbox"`,
		`title="Bookmarked lists it under Bookmarks"`,
		`<p class="repodesc">A CLI-first git forge.`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("header lacks %q", want)
		}
	}
	// the description sits inside the identity row, before the buttons
	if strings.Index(out, `class="repodesc"`) > strings.Index(out, `action="/krz/gitbay/pin"`) {
		t.Error("description renders after the buttons; it belongs in the identity row")
	}
}
