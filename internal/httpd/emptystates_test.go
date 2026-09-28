package httpd

import (
	"html/template"
	"strings"
	"testing"

	"gitbay.org/gitbay/internal/store"
	"gitbay.org/gitbay/internal/web"
)

// A signed-in viewer with zero issues gets a link to open one; a
// signed-out visitor gets only the fact, and no CLI command either way
// (#270).
func TestIssuesEmptyStateOffersOpenLinkForViewer(t *testing.T) {
	render := func(viewer string) string {
		var sb strings.Builder
		p := testRepoPage()
		p.Viewer = viewer
		if err := web.Render(&sb, "issues.html", struct {
			repoPage
			State       string
			Label       string
			Query       string
			Filters     []listFilter
			Facets      []facetGroup
			Issues      []store.Issue
			LabelColors map[string]template.CSS
			Older       string
		}{repoPage: p, State: "open"}); err != nil {
			t.Fatalf("render: %v", err)
		}
		return sb.String()
	}

	anon := render("")
	if !strings.Contains(anon, "no open issues") {
		t.Errorf("missing empty-state fact:\n%s", anon)
	}
	if strings.Contains(anon, "issues/new") {
		t.Error("signed-out visitor should not see an open-issue link")
	}
	if strings.Contains(anon, "gitbay issue create") {
		t.Error("empty state should not name a CLI command")
	}

	viewer := render("alice")
	if !strings.Contains(viewer, `href="/krz/gitbay/issues/new"`) {
		t.Errorf("signed-in viewer missing the open-issue link:\n%s", viewer)
	}
}

// An empty release list states the fact without a CLI command; the page
// already offers the create form above when the viewer can write and a
// tag is free to release (#270).
func TestReleasesEmptyStateHasNoCLICommand(t *testing.T) {
	var sb strings.Builder
	if err := web.Render(&sb, "releases.html", struct {
		repoPage
		Releases []struct {
			store.Release
			NotesHTML template.HTML
		}
		FreeTags []string
		CanWrite bool
		Notice   string
		Draft    *draft
	}{repoPage: testRepoPage()}); err != nil {
		t.Fatalf("render: %v", err)
	}
	out := sb.String()
	if !strings.Contains(out, "no releases yet") {
		t.Errorf("missing empty-state fact:\n%s", out)
	}
	if strings.Contains(out, "gitbay release create") {
		t.Error("empty state should not name a CLI command")
	}
}
